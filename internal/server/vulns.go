package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"owl/internal/catalog"
	"owl/internal/inventory"
	"owl/internal/notify"
	"owl/internal/scan"
	"owl/internal/store"
	"owl/internal/upstream"
)

// Image vulnerability scanning (milestone 15, DESIGN.md "Image scanning (Trivy)"). Findings are a separate
// signal: nothing here changes priority levels.

func (s *Server) vulnRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/vulns", s.listVulns)
	mux.HandleFunc("GET /api/v1/vulns/images/{digest}", s.vulnImage)
	mux.HandleFunc("GET /api/v1/vulns/scanner", s.scannerStatus)
	mux.HandleFunc("GET /api/v1/vulns/acceptances", s.listAcceptances)
	mux.HandleFunc("POST /api/v1/vulns/images/{digest}/rescan", s.requireAdmin(s.rescanImage))
	mux.HandleFunc("POST /api/v1/vulns/scanner/cancel", s.requireAdmin(s.cancelScan))
	mux.HandleFunc("PUT /api/v1/vulns/acceptances", s.requireAdmin(s.putAcceptance))
	mux.HandleFunc("DELETE /api/v1/vulns/acceptances", s.requireAdmin(s.deleteAcceptance))
}

// registryRefused says why the server won't pull from a target's registry ("" = it will): IP literals,
// localhost and metadata hosts, unless configured (scan.RegistryAllowed).
func (s *Server) registryRefused(t scan.Target) string {
	allowed := slices.Clone(s.cat().InternalRegistries)
	if s.Scans != nil {
		for _, c := range s.Scans.Credentials() {
			host, _, _ := strings.Cut(c.Prefix, "/")
			allowed = append(allowed, host)
		}
	}
	_, reason := scan.RegistryAllowed(t.Registry(), allowed)
	return reason
}

func (s *Server) kickScans() {
	if s.Scans != nil {
		s.Scans.Kick()
	}
}

// ScanTargets lists every running digest that can be scanned, with the platform of its cluster's nodes
// (the first cluster seen wins for a digest running on several).
func (s *Server) ScanTargets(ctx context.Context) ([]scan.Target, error) {
	cs, err := s.Store.ListClusters(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []scan.Target{}
	for _, c := range cs {
		rows, err := s.Store.CurrentImages(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			for _, ri := range r.Running {
				t, ok := scan.TargetFromImageID(ri.ImageID, c.NodeArch)
				if ok && s.registryRefused(t) == "" && !seen[t.Digest] {
					seen[t.Digest] = true
					out = append(out, t)
				}
			}
		}
	}
	return out, nil
}

// Counts per severity.
type sevCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

func (c *sevCounts) add(sev string) {
	switch sev {
	case "critical":
		c.Critical++
	case "high":
		c.High++
	case "medium":
		c.Medium++
	case "low":
		c.Low++
	default:
		c.Unknown++
	}
}

type vulnPlace struct {
	Cluster   string `json:"cluster"`
	Stage     string `json:"stage"`
	Tenant    string `json:"tenant"`
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Workload  string `json:"workload"`
	Container string `json:"container"`
}

// Image states.
const (
	stateOK          = "ok"
	stateFailed      = "failed" // the last attempt failed (an older good result may still be shown)
	stateScanning    = "scanning"
	stateQueued      = "queued"
	stateNotScanned  = "not scanned yet"
	stateUnscannable = "not scannable"
)

// vulnImageJSON is one running image (by digest; unscannable ones by their image ID).
type vulnImageJSON struct {
	Key          string      `json:"key"` // the digest, or the image ID for unscannable images
	Digest       string      `json:"digest,omitempty"`
	Ref          string      `json:"ref"`
	Repositories []string    `json:"repositories"` // what acceptances key on (catalog repository names)
	InHouse      bool        `json:"inHouse"`
	Components   []string    `json:"components"` // component ids running this image
	Places       []vulnPlace `json:"places"`
	State        string      `json:"state"`
	Queue        string      `json:"queue,omitempty"` // scanning | queued (also while an older result is shown)
	ScannedAt    *time.Time  `json:"scannedAt,omitempty"`
	Error        string      `json:"error,omitempty"`
	NeedsLogin   bool        `json:"needsLogin,omitempty"` // the last scan couldn't log in: offer "Add login"
	OS           string      `json:"os,omitempty"`
	Fixable      sevCounts   `json:"fixable"`  // findings with a fixed version, not accepted
	Total        sevCounts   `json:"total"`    // every finding, not accepted
	Accepted     int         `json:"accepted"` // findings accepted for this repository
	Confirmed    int         `json:"confirmed"`
	refused      string      // the registry host isn't allowed (see scan.RegistryAllowed)
}

type vulnRowJSON struct {
	scan.Vuln
	Accepted *store.VulnAcceptance `json:"accepted,omitempty"`
	// Expired: an acceptance existed but its date passed, so the finding counts again.
	Expired bool `json:"expiredAcceptance,omitempty"`
}

type confirmedJSON struct {
	ID        string `json:"id"`
	Component string `json:"component"`
}

// vulnData is everything one request needs, read once.
type vulnData struct {
	images  []*vulnImageJSON
	byKey   map[string]*vulnImageJSON
	results map[string]*scan.Stored
	accept  map[string]store.VulnAcceptance // repository + "\x00" + vuln id
	// owl: per digest, the advisory ids (and aliases) Owl itself lists for the component versions running
	// it — Trivy findings with those ids are shown once, as component advisories ("Trivy: confirmed").
	owl map[string]map[string]string // digest -> id -> component
	now time.Time
}

func (s *Server) loadVulns(ctx context.Context) (*vulnData, error) {
	cat := s.cat()
	inv, err := inventory.Build(ctx, s.Store, cat)
	if err != nil {
		return nil, err
	}
	cs, err := s.Store.ListClusters(ctx)
	if err != nil {
		return nil, err
	}
	arch := map[string]string{}
	for _, c := range cs {
		arch[c.Name] = c.NodeArch
	}
	accs, err := s.Store.ListVulnAcceptances(ctx)
	if err != nil {
		return nil, err
	}
	ovs, err := s.Store.ListOverrides(ctx)
	if err != nil {
		return nil, err
	}
	identify := inventory.Identifier(cat, ovs)
	d := &vulnData{byKey: map[string]*vulnImageJSON{}, results: map[string]*scan.Stored{}, accept: map[string]store.VulnAcceptance{},
		owl: map[string]map[string]string{}, now: time.Now()}
	for _, a := range accs {
		d.accept[a.Repository+"\x00"+a.VulnID] = a
	}
	queue := map[string]string{}
	if s.Scans != nil {
		d.results, queue = s.Scans.Results(), s.Scans.State()
	}
	compOf := map[*inventory.Instance]string{}
	all := []*inventory.Instance{}
	for ci := range inv.Components {
		cv := &inv.Components[ci]
		for ii := range cv.Instances {
			compOf[&cv.Instances[ii]] = cv.ID
			all = append(all, &cv.Instances[ii])
		}
	}
	for _, list := range [][]inventory.Instance{inv.Unpinned, inv.InHouse, inv.Ignored} {
		for i := range list {
			all = append(all, &list[i])
		}
	}
	for ui := range inv.Unrecognized {
		for i := range inv.Unrecognized[ui].Instances {
			all = append(all, &inv.Unrecognized[ui].Instances[i])
		}
	}
	for _, in := range all {
		comp := compOf[in] // "" for unpinned, in-house, ignored and unrecognized images
		for _, ri := range in.Running {
			if ri.ImageID == "" {
				continue
			}
			key, digest, ref, refused := ri.ImageID, "", ri.Image, ""
			if t, ok := scan.TargetFromImageID(ri.ImageID, arch[in.Cluster]); ok {
				if refused = s.registryRefused(t); refused == "" {
					key, digest, ref = t.Digest, t.Digest, t.Ref
				} else {
					ref = t.Ref
				}
			}
			img := d.byKey[key]
			if img == nil {
				img = &vulnImageJSON{Key: key, Digest: digest, Ref: ref, Repositories: []string{}, Components: []string{}, Places: []vulnPlace{}, refused: refused}
				d.byKey[key] = img
				d.images = append(d.images, img)
			}
			if in.Repository != "" && !slices.Contains(img.Repositories, in.Repository) {
				img.Repositories = append(img.Repositories, in.Repository)
			}
			if in.Status == string(catalog.StatusInHouse) {
				img.InHouse = true
			}
			if comp != "" && !slices.Contains(img.Components, comp) {
				img.Components = append(img.Components, comp)
			}
			place := vulnPlace{Cluster: in.Cluster, Stage: in.Stage, Tenant: in.Tenant, Namespace: in.Namespace,
				Kind: in.Kind, Workload: in.Workload, Container: in.Container}
			if !slices.Contains(img.Places, place) {
				img.Places = append(img.Places, place)
			}
			// The version this digest runs: pods left from a rollout may run an older one than the template.
			version := in.Version
			if ri.Image != in.Image {
				if rid, _ := identify(ri.Image); rid.Component != nil && rid.Component.ID == comp && rid.Version != nil {
					version = rid.Version.String()
				}
			}
			if digest != "" && comp != "" && version != "" && s.Upstream != nil {
				if info := s.upstreamInfo(comp); info != nil {
					ids := d.owl[digest]
					if ids == nil {
						ids = map[string]string{}
						d.owl[digest] = ids
					}
					for _, m := range upstream.AdvisoriesFor(info, version) {
						ids[m.ID] = comp
						for _, a := range m.Aliases {
							ids[a] = comp
						}
					}
				}
			}
		}
	}
	for _, img := range d.images {
		s.fillVulnImage(d, img, queue)
	}
	sort.SliceStable(d.images, func(i, j int) bool {
		a, b := d.images[i], d.images[j]
		if a.InHouse != b.InHouse {
			return a.InHouse
		}
		if ka, kb := a.Fixable.Critical*1000+a.Fixable.High, b.Fixable.Critical*1000+b.Fixable.High; ka != kb {
			return ka > kb
		}
		return a.Ref < b.Ref
	})
	return d, nil
}

// fillVulnImage sets the state and the counts.
func (s *Server) fillVulnImage(d *vulnData, img *vulnImageJSON, queue map[string]string) {
	if img.Digest == "" {
		img.State = stateUnscannable
		img.Error = "not in a registry: built or imported on the node (no registry digest)"
		if img.refused != "" {
			img.Error = img.refused
		}
		return
	}
	img.Queue = queue[img.Digest]
	st := d.results[img.Digest]
	switch {
	case s.Scans == nil:
		img.State, img.Error = stateNotScanned, "scanning is off on this server"
	case st == nil && img.Queue != "":
		img.State = img.Queue
	case st == nil:
		img.State = stateNotScanned
	case st.Error != "" && st.Result == nil:
		img.State, img.Error = stateFailed, st.Error
	case st.Error != "": // the last rescan failed: the older result is still shown
		img.State, img.Error = stateFailed, st.Error
	default:
		img.State = stateOK
	}
	img.NeedsLogin = st != nil && scan.IsAuthFailure(st.Error)
	if st == nil || st.Result == nil {
		return
	}
	img.ScannedAt, img.OS = st.ScannedAt, st.Result.OS
	for _, row := range d.rows(img, st.Result) {
		switch {
		case row.Accepted != nil:
			img.Accepted++
		case row.confirmed:
			img.Confirmed++
		default:
			img.Total.add(row.Severity)
			if row.Fixed != "" {
				img.Fixable.add(row.Severity)
			}
		}
	}
}

type vulnRow struct {
	vulnRowJSON
	confirmed bool
	component string
}

// rows classifies every finding of an image: accepted (for one of its repositories, not expired),
// confirmed (Owl lists it as a component advisory) or counted.
func (d *vulnData) rows(img *vulnImageJSON, res *scan.Result) []vulnRow {
	out := make([]vulnRow, 0, len(res.Vulns))
	owl := d.owl[img.Digest]
	for _, v := range res.Vulns {
		row := vulnRow{vulnRowJSON: vulnRowJSON{Vuln: v}}
		if comp, ok := owl[v.ID]; ok {
			row.confirmed, row.component = true, comp
		}
		for _, repo := range img.Repositories {
			if a, ok := d.accept[repo+"\x00"+v.ID]; ok {
				if a.ExpiresAt != nil && !a.ExpiresAt.After(d.now) {
					row.Expired = true
					continue
				}
				row.Accepted = &a
				break
			}
		}
		out = append(out, row)
	}
	return out
}

// VulnDigest summarizes in-house images for the weekly digest (nil = scanning off).
func (s *Server) VulnDigest(ctx context.Context) (*notify.VulnSummary, error) {
	if s.Scans == nil {
		return nil, nil
	}
	d, err := s.loadVulns(ctx)
	if err != nil {
		return nil, err
	}
	v := &notify.VulnSummary{}
	for _, img := range d.images {
		if !img.InHouse {
			continue
		}
		v.Images++
		if img.ScannedAt == nil {
			v.NotScanned++
			continue
		}
		v.Critical += img.Fixable.Critical
		v.High += img.Fixable.High
		if img.Fixable.Critical+img.Fixable.High > 0 {
			v.Affected++
		}
	}
	for _, e := range s.Scans.Credentials().Expiring(time.Now()) {
		v.Expiring = append(v.Expiring, notify.CredentialExpiry{Name: e.Login, ExpiresAt: e.ExpiresAt, Expired: e.Expired})
	}
	return v, nil
}

func (s *Server) listVulns(w http.ResponseWriter, r *http.Request) {
	d, err := s.loadVulns(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]*vulnImageJSON, 0, len(d.images))
	out = append(out, d.images...)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": s.Scans != nil, "images": out})
}

func (s *Server) vulnImage(w http.ResponseWriter, r *http.Request) {
	digest := r.PathValue("digest")
	if !scan.ValidDigest(digest) {
		httpError(w, http.StatusBadRequest, "digest: sha256:<64 hex characters>")
		return
	}
	d, err := s.loadVulns(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	img := d.byKey[digest]
	if img == nil {
		httpError(w, http.StatusNotFound, "this image isn't running in any cluster")
		return
	}
	vulns, confirmed := []vulnRowJSON{}, []confirmedJSON{}
	if st := d.results[digest]; st != nil && st.Result != nil {
		for _, row := range d.rows(img, st.Result) {
			if row.confirmed && row.Accepted == nil {
				if !slices.ContainsFunc(confirmed, func(c confirmedJSON) bool { return c.ID == row.ID }) {
					confirmed = append(confirmed, confirmedJSON{ID: row.ID, Component: row.component})
				}
				continue
			}
			vulns = append(vulns, row.vulnRowJSON)
		}
	}
	rank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "unknown": 4}
	sort.SliceStable(vulns, func(i, j int) bool {
		a, b := vulns[i], vulns[j]
		if (a.Accepted == nil) != (b.Accepted == nil) {
			return a.Accepted == nil
		}
		if rank[a.Severity] != rank[b.Severity] {
			return rank[a.Severity] < rank[b.Severity]
		}
		if (a.Fixed != "") != (b.Fixed != "") {
			return a.Fixed != ""
		}
		return a.ID < b.ID
	})
	writeJSON(w, http.StatusOK, map[string]any{"image": img, "vulns": vulns, "confirmed": confirmed})
}

// componentScans: the scanned images a component runs, with the ids Trivy confirmed (for its page).
func (s *Server) componentScans(ctx context.Context, id string) ([]*vulnImageJSON, []string, error) {
	d, err := s.loadVulns(ctx)
	if err != nil {
		return nil, nil, err
	}
	imgs, confirmed := []*vulnImageJSON{}, []string{}
	for _, img := range d.images {
		if !slices.Contains(img.Components, id) {
			continue
		}
		imgs = append(imgs, img)
		st := d.results[img.Digest]
		if img.Digest == "" || st == nil || st.Result == nil {
			continue
		}
		for _, row := range d.rows(img, st.Result) {
			if row.confirmed && row.component == id && !slices.Contains(confirmed, row.ID) {
				confirmed = append(confirmed, row.ID)
			}
		}
	}
	return imgs, confirmed, nil
}

func (s *Server) scannerStatus(w http.ResponseWriter, r *http.Request) {
	if s.Scans == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "reason": s.ScansOff})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "status": s.Scans.Status()})
}

func (s *Server) rescanImage(w http.ResponseWriter, r *http.Request) {
	digest := r.PathValue("digest")
	if !scan.ValidDigest(digest) {
		httpError(w, http.StatusBadRequest, "digest: sha256:<64 hex characters>")
		return
	}
	if s.Scans == nil {
		httpError(w, http.StatusConflict, "scanning is off on this server")
		return
	}
	queued, err := s.Scans.Rescan(r.Context(), digest)
	if errors.Is(err, scan.ErrNotRunning) {
		httpError(w, http.StatusNotFound, err.Error())
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	if queued {
		s.audit(r, "image rescan requested", "digest", digest)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"queued": queued})
}

func (s *Server) cancelScan(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Digest string `json:"digest"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if !scan.ValidDigest(b.Digest) {
		httpError(w, http.StatusBadRequest, "digest: sha256:<64 hex characters>")
		return
	}
	if s.Scans == nil {
		httpError(w, http.StatusConflict, "scanning is off on this server")
		return
	}
	j, err := s.Scans.Cancel(b.Digest)
	if errors.Is(err, scan.ErrNotQueued) {
		httpError(w, http.StatusNotFound, err.Error())
		return
	}
	s.audit(r, "image scan cancelled", "ref", j.Ref, "reason", j.Reason)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAcceptances(w http.ResponseWriter, r *http.Request) {
	as, err := s.Store.ListVulnAcceptances(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, as)
}

var reVulnID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{1,99}$`)

func validRepository(s string) bool {
	if s == "" || len(s) > 300 {
		return false
	}
	for _, c := range s {
		if unicode.IsSpace(c) || unicode.IsControl(c) || c == '@' {
			return false
		}
	}
	return true
}

func (s *Server) putAcceptance(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Repository string `json:"repository"`
		VulnID     string `json:"vulnId"`
		Reason     string `json:"reason"`
		ExpiresAt  string `json:"expiresAt"` // YYYY-MM-DD (end of that day, UTC) or "" = no expiry
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	b.Reason = strings.TrimSpace(b.Reason)
	switch {
	case !validRepository(b.Repository):
		httpError(w, http.StatusBadRequest, "repository: an image repository like registry.example.com/team/app")
		return
	case !reVulnID.MatchString(b.VulnID):
		httpError(w, http.StatusBadRequest, "vulnId: a vulnerability id like CVE-2026-1234")
		return
	case b.Reason == "" || utf8.RuneCountInString(b.Reason) > 500 || strings.ContainsFunc(b.Reason, func(c rune) bool { return unicode.IsControl(c) && c != '\n' }):
		httpError(w, http.StatusBadRequest, "reason: required, up to 500 characters")
		return
	}
	a := store.VulnAcceptance{Repository: b.Repository, VulnID: b.VulnID, Reason: b.Reason}
	if b.ExpiresAt != "" {
		day, err := time.Parse("2006-01-02", b.ExpiresAt)
		if err != nil {
			httpError(w, http.StatusBadRequest, "expiresAt: a date like 2027-03-31, or empty for no expiry")
			return
		}
		end := day.Add(24*time.Hour - time.Millisecond).UTC()
		if !end.After(time.Now()) {
			httpError(w, http.StatusBadRequest, "expiresAt: must be in the future")
			return
		}
		a.ExpiresAt = &end
	}
	if sess := sessionFrom(r); sess != nil {
		a.CreatedBy = sess.User.Email
	}
	if err := s.Store.PutVulnAcceptance(r.Context(), a); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "vulnerability accepted", "repository", a.Repository, "vuln", a.VulnID, "reason", a.Reason, "expires", b.ExpiresAt)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteAcceptance(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Repository string `json:"repository"`
		VulnID     string `json:"vulnId"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	err := s.Store.DeleteVulnAcceptance(r.Context(), b.Repository, b.VulnID)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no such acceptance")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "vulnerability acceptance removed", "repository", b.Repository, "vuln", b.VulnID)
	w.WriteHeader(http.StatusNoContent)
}
