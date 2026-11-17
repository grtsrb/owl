package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"owl/internal/catalog"
	"owl/internal/inventory"
	"owl/internal/registry"
	"owl/internal/store"
	"owl/internal/upstream"
)

// Components added in the UI (milestone 11). The merged catalog (built-in + custom) is swapped
// atomically on every change; a request or check uses one snapshot (cat()).

type liveCatalog struct {
	cat    *catalog.Catalog
	custom []customView
}

type customView struct {
	Component catalog.Component   `json:"component"`
	State     catalog.CustomState `json:"state"`
	CreatedAt time.Time           `json:"createdAt"`
	UpdatedAt time.Time           `json:"updatedAt"`
}

// cat is the current catalog: built-in plus the active custom components.
func (s *Server) cat() *catalog.Catalog {
	if l := s.live.Load(); l != nil {
		return l.cat
	}
	return s.Catalog
}

// CurrentCatalog is cat() for the notifier and the summary manager.
func (s *Server) CurrentCatalog() *catalog.Catalog { return s.cat() }

// ReloadCatalog rebuilds the merged catalog from the database (at startup, after a change or a restore).
func (s *Server) ReloadCatalog(ctx context.Context) error {
	s.reloadM.Lock()
	defer s.reloadM.Unlock()
	rows, err := s.Store.ListCustomComponents(ctx)
	if err != nil {
		return err
	}
	var comps []catalog.Component
	var views []customView
	bad := map[string]string{}
	for _, r := range rows {
		// Rows can come from a backup someone uploaded: validate them like the API does, so fields
		// the UI can't set (source, osv, …) or malformed names never take effect.
		var c catalog.Component
		if err := json.Unmarshal(r.Data, &c); err != nil {
			bad[r.ID] = "stored data unreadable: " + err.Error()
			c = catalog.Component{ID: r.ID, Name: r.ID}
		} else if c.ID = r.ID; catalog.ValidateCustom(&c) != nil {
			bad[r.ID] = "stored data invalid: " + catalog.ValidateCustom(&c).Error()
		} else {
			comps = append(comps, c)
		}
		views = append(views, customView{Component: c, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
	}
	merged, states := catalog.Merge(s.Catalog, comps)
	byID := map[string]catalog.CustomState{}
	for _, st := range states {
		byID[st.ID] = st
	}
	for i := range views {
		id := views[i].Component.ID
		views[i].State = byID[id]
		views[i].State.ID = id
		if p, ok := bad[id]; ok {
			views[i].State.Problem = p
		}
		if st := views[i].State; !st.Active {
			s.Log.Warn("custom component not in effect", "component", id, "replacedBy", st.Replaced, "problem", st.Problem)
		}
	}
	s.live.Store(&liveCatalog{cat: merged, custom: views})
	return nil
}

func (s *Server) catalogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/catalog/custom", s.listCustom)
	mux.HandleFunc("POST /api/v1/catalog/custom", s.requireAdmin(s.createCustom))
	mux.HandleFunc("PUT /api/v1/catalog/custom/{id}", s.requireAdmin(s.updateCustom))
	mux.HandleFunc("GET /api/v1/catalog/custom/{id}/impact", s.requireAdmin(s.customImpact))
	mux.HandleFunc("DELETE /api/v1/catalog/custom/{id}", s.requireAdmin(s.deleteCustom))
	mux.HandleFunc("POST /api/v1/catalog/preview", s.requireAdmin(s.previewCustom))
	mux.HandleFunc("GET /api/v1/catalog/suggest", s.requireAdmin(s.suggestCustom))
}

func (s *Server) customViews() []customView {
	if l := s.live.Load(); l != nil && l.custom != nil {
		return l.custom
	}
	return []customView{}
}

func (s *Server) listCustom(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.customViews())
}

type customBody struct {
	Component catalog.Component `json:"component"`
}

// readCustom decodes and validates a component; on update the id comes from the path.
func (s *Server) readCustom(w http.ResponseWriter, r *http.Request, id string) (*catalog.Component, bool) {
	var b customBody
	if !decodeJSON(w, r, &b) {
		return nil, false
	}
	c := b.Component
	if id != "" {
		c.ID = id
	}
	if err := catalog.ValidateCustom(&c); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return &c, true
}

// claimConflict: an image of c already belongs to another component (built-in or custom).
func (s *Server) claimConflict(c *catalog.Component) string {
	cat := s.cat()
	for _, img := range c.Images {
		if owner := cat.Claimant(img); owner != "" && owner != c.ID {
			kind := "built-in component"
			if oc := cat.ByID(owner); oc != nil && oc.Custom {
				kind = "component"
			}
			return fmt.Sprintf("%s already belongs to the %s %q", img, kind, owner)
		}
	}
	return ""
}

func (s *Server) createCustom(w http.ResponseWriter, r *http.Request) {
	c, ok := s.readCustom(w, r, "")
	if !ok {
		return
	}
	if s.cat().ByID(c.ID) != nil { // built-in or added (an inactive added one: ErrExists below)
		httpError(w, http.StatusConflict, fmt.Sprintf("a component with id %q exists already", c.ID))
		return
	}
	if msg := s.claimConflict(c); msg != "" {
		httpError(w, http.StatusConflict, msg)
		return
	}
	data, _ := json.Marshal(c)
	if err := s.Store.CreateCustomComponent(r.Context(), c.ID, data); errors.Is(err, store.ErrExists) {
		httpError(w, http.StatusConflict, fmt.Sprintf("a component with id %q exists already", c.ID))
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "component added", "component", c.ID, "github", c.GitHub, "eol", c.EOL, "images", strings.Join(c.Images, ","))
	s.customChanged(r.Context(), c.ID)
	writeJSON(w, http.StatusCreated, s.customViews())
}

func (s *Server) updateCustom(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, ok := s.readCustom(w, r, id)
	if !ok {
		return
	}
	if s.Catalog.ByID(id) != nil {
		httpError(w, http.StatusConflict, "built-in components can't be edited")
		return
	}
	if msg := s.claimConflict(c); msg != "" {
		httpError(w, http.StatusConflict, msg)
		return
	}
	old := s.cat().ByID(id)
	data, _ := json.Marshal(c)
	if err := s.Store.UpdateCustomComponent(r.Context(), id, data); err != nil {
		s.storeError(w, err)
		return
	}
	if old != nil && (old.GitHub != c.GitHub || old.EOL != c.EOL || old.TagPrefix != c.TagPrefix ||
		old.ReleaseTagPrefix != c.ReleaseTagPrefix || old.VersionPattern != c.VersionPattern) {
		s.forgetSummaries(r.Context(), id) // written from another project's (or other releases') notes
	}
	s.audit(r, "component edited", "component", id, "github", c.GitHub, "eol", c.EOL, "images", strings.Join(c.Images, ","))
	s.customChanged(r.Context(), id)
	writeJSON(w, http.StatusOK, s.customViews())
}

// builtinOwnsID: a built-in component has the same id, so the policy and overrides under it are the
// built-in's — deleting the replaced custom entry must leave them alone.
func (s *Server) builtinOwnsID(id string) bool { return s.Catalog.ByID(id) != nil }

func (s *Server) customImpact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	imp, err := s.Store.CustomDeleteImpact(r.Context(), id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if s.builtinOwnsID(id) {
		imp = &store.CustomDeleted{Overrides: []string{}}
	}
	writeJSON(w, http.StatusOK, imp)
}

func (s *Server) deleteCustom(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	builtin := s.builtinOwnsID(id)
	imp, err := s.Store.DeleteCustomComponent(r.Context(), id, !builtin)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if !builtin {
		s.forgetSummaries(r.Context(), id)
	}
	s.audit(r, "component deleted", "component", id, "policyRemoved", imp.Policy, "overridesRemoved", strings.Join(imp.Overrides, ","))
	s.customChanged(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"removed": imp, "components": s.customViews()})
}

// customChanged reloads the catalog, fetches the component's upstream data at once (in the background;
// cached, so cheap) and lets notifications and summaries look again.
func (s *Server) customChanged(ctx context.Context, id string) {
	if err := s.ReloadCatalog(ctx); err != nil {
		s.Log.Error("catalog reload failed", "err", err)
	}
	comp := s.cat().ByID(id)
	go func() {
		ctx := context.Background()
		var info *upstream.Info
		if comp != nil && s.Upstream != nil {
			info = s.Upstream.Refresh(ctx, comp)
		}
		s.mu.Lock()
		// Only if the catalog still has exactly this version of the component: a later edit (or delete)
		// has its own fetch running, and an older one must not overwrite it.
		if cur := s.cat().ByID(id); s.upstream != nil && cur == comp {
			next := make(map[string]*upstream.Info, len(s.upstream)+1)
			for k, v := range s.upstream {
				next[k] = v
			}
			if info != nil {
				next[id] = info
			} else {
				delete(next, id)
			}
			s.upstream = next
		}
		s.mu.Unlock()
		s.kickNotify()
		if s.Summaries != nil {
			s.Summaries.Kick()
		}
	}()
}

func (s *Server) forgetSummaries(ctx context.Context, id string) {
	if err := s.Store.DeleteSummaries(ctx, id); err != nil {
		s.Log.Error("summaries not removed", "component", id, "err", err)
	}
	if s.Summaries != nil {
		s.Summaries.Forget(id)
	}
}

// --- preview ---

type previewTag struct {
	Image    string   `json:"image"`
	Clusters []string `json:"clusters"`
	Status   string   `json:"status"`
	Version  string   `json:"version,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

type previewResult struct {
	Upstream     *upstream.Info `json:"upstream,omitempty"`
	GitHubLatest string         `json:"githubLatest,omitempty"`
	EOLLatest    string         `json:"eolLatest,omitempty"`
	Tags         []previewTag   `json:"tags"`
	Warnings     []string       `json:"warnings"`
	Conflict     string         `json:"conflict,omitempty"`
}

// previewCustom shows what Owl would know about a component before it's saved: upstream data, how the
// running tags would be read, and warnings (likely different projects, no releases, unreadable tags).
func (s *Server) previewCustom(w http.ResponseWriter, r *http.Request) {
	c, ok := s.readCustom(w, r, "")
	if !ok {
		return
	}
	res := previewResult{Tags: []previewTag{}, Warnings: []string{}, Conflict: s.claimConflict(c)}
	solo, _ := catalog.Merge(&catalog.Catalog{}, []catalog.Component{*c})
	comp := solo.ByID(c.ID)

	cat := s.cat()
	inv, err := inventory.Build(r.Context(), s.Store, cat)
	if err != nil {
		s.fail(w, err)
		return
	}
	clustersOf := map[string]map[string]bool{}
	collect := func(in inventory.Instance) {
		if clustersOf[in.Image] == nil {
			clustersOf[in.Image] = map[string]bool{}
		}
		clustersOf[in.Image][in.Cluster] = true
	}
	for _, cv := range inv.Components {
		for _, in := range cv.Instances {
			collect(in)
		}
	}
	for _, list := range [][]inventory.Instance{inv.Unpinned, inv.InHouse, inv.Ignored} {
		for _, in := range list {
			collect(in)
		}
	}
	for _, u := range inv.Unrecognized {
		for _, in := range u.Instances {
			collect(in)
		}
	}
	readable := 0
	for img, cls := range clustersOf {
		if strings.HasPrefix(img, "kubernetes") || img == "" {
			continue
		}
		id := solo.Identify(img)
		if id.Component == nil {
			continue
		}
		t := previewTag{Image: img, Status: string(id.Status), Reason: id.Reason}
		if id.Version != nil {
			t.Version = id.Version.String()
		}
		if id.Status == catalog.StatusIdentified {
			readable++
		}
		for c := range cls {
			t.Clusters = append(t.Clusters, c)
		}
		sort.Strings(t.Clusters)
		res.Tags = append(res.Tags, t)
	}
	sort.Slice(res.Tags, func(i, j int) bool { return res.Tags[i].Image < res.Tags[j].Image })
	switch {
	case len(res.Tags) == 0:
		res.Warnings = append(res.Warnings, "None of these images runs in a cluster right now.")
	case readable == 0:
		res.Warnings = append(res.Warnings, "None of the running tags can be read as an exact version — set a tag prefix or a version pattern, or pin versioned tags.")
	}

	if s.Upstream == nil {
		res.Warnings = append(res.Warnings, "Upstream checks are off on this server: no release data to preview.")
		writeJSON(w, http.StatusOK, res)
		return
	}
	ctx := r.Context()
	res.Upstream = s.Upstream.Refresh(ctx, comp)
	if comp.EOL != "" {
		res.EOLLatest = res.Upstream.LatestStable
		res.GitHubLatest = s.Upstream.GitHubLatest(ctx, comp)
		if a, okA := catalog.ParseVersion(res.EOLLatest, ""); okA {
			if b, okB := catalog.ParseVersion(res.GitHubLatest, ""); okB && (a.Major != b.Major || a.Minor != b.Minor) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("endoflife.date's newest release is %s but GitHub's is %s — make sure %q on endoflife.date is the same project as %s.",
					res.EOLLatest, res.GitHubLatest, comp.EOL, comp.GitHub))
			}
		}
	} else {
		res.GitHubLatest = res.Upstream.LatestStable
	}
	if len(res.Upstream.Lines) == 0 {
		res.Warnings = append(res.Warnings, "Owl found no releases for "+comp.GitHub+" — check the repository (and the release tag prefix).")
	}
	if res.Upstream.Archived {
		res.Warnings = append(res.Warnings, comp.GitHub+" is archived on GitHub.")
	}
	for _, e := range res.Upstream.Errors {
		res.Warnings = append(res.Warnings, "Upstream: "+e)
	}
	if len(res.Upstream.Lines) > 8 {
		res.Upstream.Lines = res.Upstream.Lines[:8]
	}
	res.Upstream.Advisories = nil // counts aren't needed here, and the list can be long
	writeJSON(w, http.StatusOK, res)
}

// --- suggestions ---

type suggestion struct {
	Image       string                `json:"image"`
	Name        string                `json:"name"`
	ID          string                `json:"id"`
	GitHub      []githubSuggestion    `json:"github"`
	EOL         []upstream.EOLProduct `json:"eol"`
	LabelSource string                `json:"labelSource,omitempty"`
	LabelError  string                `json:"labelError,omitempty"`
}

type githubSuggestion struct {
	Repo string `json:"repo"`
	From string `json:"from"` // "label" | "same-name" (a GitHub repository with the image's owner/name exists)
}

var reNonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// suggestCustom: for an unrecognized image, propose a name, id, GitHub repo (from the image's OCI source
// label on public registries, or the ghcr.io owner) and endoflife.date products with a matching name.
// Only on demand; a person always confirms.
func (s *Server) suggestCustom(w http.ResponseWriter, r *http.Request) {
	name, err := catalog.NormalizeImage(r.URL.Query().Get("image"))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	ref := catalog.ParseRef(name + ":latest")
	seg := ref.Repository[strings.LastIndex(ref.Repository, "/")+1:]
	out := suggestion{Image: name, Name: seg, GitHub: []githubSuggestion{}, EOL: []upstream.EOLProduct{}}
	out.ID = strings.Trim(reNonSlug.ReplaceAllString(seg, "-"), "-")

	if s.Registry != nil {
		tag := s.runningTag(r.Context(), name)
		if _, ok := registry.Hosts[ref.Registry]; !ok {
			out.LabelError = "not a public registry: no label lookup"
		} else if labels, err := s.Registry.Labels(r.Context(), ref.Registry, ref.Repository, tag); err != nil {
			out.LabelError = err.Error()
		} else {
			out.LabelSource = labels[registry.SourceLabel]
			if repo := registry.GitHubRepo(out.LabelSource); repo != "" {
				out.GitHub = append(out.GitHub, githubSuggestion{repo, "label"})
			}
			if t := strings.TrimSpace(labels["org.opencontainers.image.title"]); t != "" && len(t) <= 60 && !strings.ContainsAny(t, "<>`\r\n") {
				out.Name = t
			}
		}
	}
	// No label: a GitHub repository with exactly the image's owner/name (ghcr.io images are published
	// by that GitHub owner; Docker Hub namespaces often match). One exact lookup, not a search.
	if len(out.GitHub) == 0 && s.Upstream != nil && strings.Count(ref.Repository, "/") == 1 && !strings.HasPrefix(ref.Repository, "library/") &&
		(ref.Registry == "ghcr.io" || ref.Registry == "docker.io" || ref.Registry == "quay.io") {
		if repo := s.Upstream.GitHubRepo(r.Context(), ref.Repository); repo != "" {
			out.GitHub = append(out.GitHub, githubSuggestion{repo, "same-name"})
		}
	}
	if s.Upstream != nil {
		if prods, err := s.Upstream.EOLProducts(r.Context()); err == nil {
			if m := matchProducts(prods, seg); len(m) > 0 {
				out.EOL = m
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// runningTag picks a tag of the repository that runs in a cluster (its labels are what matters), else latest.
func (s *Server) runningTag(ctx context.Context, repo string) string {
	inv, err := inventory.Build(ctx, s.Store, s.cat())
	if err != nil {
		return "latest"
	}
	all := append(append(append([]inventory.Instance{}, inv.Unpinned...), inv.InHouse...), inv.Ignored...)
	for _, u := range inv.Unrecognized {
		all = append(all, u.Instances...)
	}
	for _, cv := range inv.Components {
		all = append(all, cv.Instances...)
	}
	for _, in := range all {
		if r := catalog.ParseRef(in.Image); r.Name() == repo && r.Tag != "" {
			return r.Tag
		}
	}
	return "latest"
}

// matchProducts: endoflife.date products whose slug or an alias equals the image name, then ones that
// contain it (or the other way round, for names of 4+ letters). At most 5.
func matchProducts(prods []upstream.EOLProduct, seg string) []upstream.EOLProduct {
	seg = strings.ToLower(seg)
	var exact, near []upstream.EOLProduct
	for _, p := range prods {
		names := append([]string{p.Slug}, p.Aliases...)
		hit := 0
		for _, n := range names {
			n = strings.ToLower(n)
			switch {
			case n == seg:
				hit = 2
			case hit == 0 && len(seg) >= 4 && len(n) >= 4 && (strings.Contains(n, seg) || strings.Contains(seg, n)):
				hit = 1
			}
		}
		switch hit {
		case 2:
			exact = append(exact, p)
		case 1:
			near = append(near, p)
		}
	}
	out := append(exact, near...)
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}
