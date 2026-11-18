package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"owl/deploy"
	"owl/internal/auth"
	"owl/internal/store"
)

var (
	// The agent image is pasted into a YAML manifest: no whitespace, quotes or YAML syntax.
	reImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}$`)
	// A repository as catalog.Ref.Name() prints it: registry (may have a port) / path, no tag or digest.
	reRepository = regexp.MustCompile(`^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+$`)
)

const (
	settingAgentImage = "agentImage"
	settingServerURL  = "serverURL"
)

// decodeJSON enforces JSON-only writes (with no CORS headers, a cross-site page can't send them) and
// a small body.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		httpError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// storeError maps store errors to HTTP statuses; anything unknown is a 500.
func (s *Server) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrExists):
		httpError(w, http.StatusConflict, "already exists")
	case errors.Is(err, store.ErrInUse):
		httpError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrUnknownStage), errors.Is(err, store.ErrUnknownTenant), errors.Is(err, store.ErrInvalid):
		httpError(w, http.StatusBadRequest, err.Error())
	default:
		s.fail(w, err)
	}
}

// audit records an admin action in the audit log (DB) and the application log, with the signed-in
// user as the actor. args are key/value pairs, like slog's.
func (s *Server) audit(r *http.Request, action string, args ...any) {
	actor := ""
	if sess := sessionFrom(r); sess != nil {
		actor = sess.User.Email
		if sess.APIToken != nil {
			actor += " (token: " + sess.APIToken.Name + ")"
		}
	}
	remote := s.clientIP(r)
	s.Log.Info("audit: "+action, append(args, "by", actor, "remote", remote, "req", requestID(r))...)
	var parts []string
	for i := 0; i+1 < len(args); i += 2 {
		parts = append(parts, fmt.Sprintf("%v=%v", args[i], args[i+1]))
	}
	// The action already happened: a failed audit write is logged, not turned into an error response.
	if err := s.Store.AddAudit(context.WithoutCancel(r.Context()), store.AuditEntry{Action: action, Details: strings.Join(parts, " "), Remote: remote, Actor: actor}); err != nil {
		s.Log.Error("audit log write failed", "action", action, "err", err)
	}
}

func toClusterJSON(c *store.Cluster) clusterJSON {
	return clusterJSON{c.Name, string(c.Stage), c.Tenant, c.KubernetesVersion, c.AgentVersion, c.LastSeenAt}
}

// --- clusters ---

type clusterBody struct {
	Name   string `json:"name"`
	Stage  string `json:"stage"`
	Tenant string `json:"tenant"`
}

func (b *clusterBody) validate(needName bool) string {
	b.Tenant = strings.TrimSpace(b.Tenant)
	switch {
	case needName && !store.ValidClusterName(b.Name):
		return "name: " + store.ClusterNameRule
	case !store.ValidStageName(b.Stage):
		return "stage: pick one of the configured stages"
	case !store.ValidTenantName(b.Tenant):
		return "tenant: " + store.TenantNameRule
	}
	return ""
}

// createCluster registers a cluster and returns its agent token — the only time it's ever shown.
func (s *Server) createCluster(w http.ResponseWriter, r *http.Request) {
	var b clusterBody
	if !decodeJSON(w, r, &b) {
		return
	}
	if msg := b.validate(true); msg != "" {
		httpError(w, http.StatusBadRequest, msg)
		return
	}
	token, hash, prefix := auth.NewToken()
	c, err := s.Store.CreateClusterWithToken(r.Context(), b.Name, store.Stage(b.Stage), b.Tenant, hash, prefix)
	if err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "cluster added", "cluster", c.Name, "stage", c.Stage, "tenant", c.Tenant)
	writeJSON(w, http.StatusCreated, map[string]any{"cluster": toClusterJSON(c), "token": token})
}

func (s *Server) updateCluster(w http.ResponseWriter, r *http.Request) {
	var b clusterBody
	if !decodeJSON(w, r, &b) {
		return
	}
	if msg := b.validate(false); msg != "" {
		httpError(w, http.StatusBadRequest, msg)
		return
	}
	c, err := s.Store.UpdateCluster(r.Context(), r.PathValue("name"), store.Stage(b.Stage), b.Tenant)
	if err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "cluster edited", "cluster", c.Name, "stage", c.Stage, "tenant", c.Tenant)
	s.kickNotify()
	writeJSON(w, http.StatusOK, toClusterJSON(c))
}

// rotateToken revokes the cluster's tokens and issues a new one; the agent is rejected until its
// Secret is updated and the pod restarted.
func (s *Server) rotateToken(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.GetCluster(r.Context(), r.PathValue("name"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	token, hash, prefix := auth.NewToken()
	if err := s.Store.RotateToken(r.Context(), c.ID, hash, prefix); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "token rotated", "cluster", c.Name)
	writeJSON(w, http.StatusOK, map[string]any{"cluster": toClusterJSON(c), "token": token})
}

func (s *Server) deleteCluster(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.Store.DeleteCluster(r.Context(), name); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "cluster deleted", "cluster", name)
	s.kickNotify()
	writeJSON(w, http.StatusOK, map[string]string{"deleted": name})
}

const exportFormat = "owl/cluster-export"

// ClusterExport is everything about one cluster except its token hashes (useless once deleted).
// Milestone 7 adds importing it back.
type ClusterExport struct {
	Format     string           `json:"format"` // "owl/cluster-export"
	Version    int              `json:"version"`
	ExportedAt time.Time        `json:"exportedAt"`
	Cluster    clusterJSON      `json:"cluster"`
	CreatedAt  time.Time        `json:"createdAt"`
	Images     []store.ImageRow `json:"images"`
	Changes    []store.Change   `json:"changes"`
}

// exportCluster is admin-only: the read API is open on the private network for now, but a full
// history dump is the kind of thing to keep behind the token.
func (s *Server) exportCluster(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.GetCluster(r.Context(), r.PathValue("name"))
	if err != nil {
		s.storeError(w, err)
		return
	}
	imgs, err := s.Store.CurrentImages(r.Context(), c.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	changes, err := s.Store.AllChanges(r.Context(), c.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if imgs == nil {
		imgs = []store.ImageRow{}
	}
	if changes == nil {
		changes = []store.Change{}
	}
	s.audit(r, "cluster exported", "cluster", c.Name)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cluster-%s-%s.json"`, c.Name, time.Now().UTC().Format("20060102")))
	writeJSON(w, http.StatusOK, ClusterExport{Format: exportFormat, Version: 1,
		ExportedAt: time.Now().UTC(), Cluster: toClusterJSON(c), CreatedAt: c.CreatedAt, Images: imgs, Changes: changes})
}

// --- stages ---

func (s *Server) listStages(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.ListStages(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) createStage(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if !store.ValidStageName(b.Name) {
		httpError(w, http.StatusBadRequest, "stage: "+store.StageNameRule)
		return
	}
	if err := s.Store.CreateStage(r.Context(), b.Name); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "stage added", "stage", b.Name)
	s.listStages(w, r)
}

func (s *Server) reorderStages(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Order []string `json:"order"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if err := s.Store.ReorderStages(r.Context(), b.Order); err != nil {
		s.storeError(w, err) // ErrInvalid / ErrUnknownStage -> 400, anything else -> 500 (logged)
		return
	}
	s.audit(r, "stages reordered", "order", strings.Join(b.Order, ","))
	s.listStages(w, r)
}

func (s *Server) deleteStage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.Store.DeleteStage(r.Context(), name); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "stage deleted", "stage", name)
	s.listStages(w, r)
}

// --- tenants ---

func (s *Server) listTenants(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Store.ListTenants(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func tenantName(w http.ResponseWriter, raw string) (string, bool) {
	n := strings.TrimSpace(raw)
	if !store.ValidTenantName(n) {
		httpError(w, http.StatusBadRequest, "tenant: "+store.TenantNameRule)
		return "", false
	}
	return n, true
}

func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	n, ok := tenantName(w, b.Name)
	if !ok {
		return
	}
	if err := s.Store.CreateTenant(r.Context(), n); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "tenant added", "tenant", n)
	s.listTenants(w, r)
}

func (s *Server) renameTenant(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	n, ok := tenantName(w, b.Name)
	if !ok {
		return
	}
	from := r.PathValue("name")
	if err := s.Store.RenameTenant(r.Context(), from, n); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "tenant renamed", "from", from, "to", n)
	s.listTenants(w, r)
}

func (s *Server) deleteTenant(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.Store.DeleteTenant(r.Context(), name); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "tenant deleted", "tenant", name)
	s.listTenants(w, r)
}

// --- catalog overrides ---

func (s *Server) listOverrides(w http.ResponseWriter, r *http.Request) {
	os, err := s.Store.ListOverrides(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, os)
}

func (s *Server) setOverride(w http.ResponseWriter, r *http.Request) {
	var o store.CatalogOverride
	if !decodeJSON(w, r, &o) {
		return
	}
	o.Repository = strings.TrimSpace(o.Repository)
	switch {
	case len(o.Repository) > 255 || !reRepository.MatchString(o.Repository):
		httpError(w, http.StatusBadRequest, "repository: registry/repository without tag or digest")
		return
	case !o.Kind.Valid():
		httpError(w, http.StatusBadRequest, "kind: map, inhouse or ignore")
		return
	case o.Kind == store.OverrideMap && s.cat().ByID(o.Component) == nil:
		httpError(w, http.StatusBadRequest, "component: unknown component id")
		return
	case o.Kind != store.OverrideMap:
		o.Component = ""
	}
	if err := s.Store.SetOverride(r.Context(), o); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "catalog override set", "repository", o.Repository, "kind", o.Kind, "component", o.Component)
	s.kickNotify()
	s.listOverrides(w, r)
}

func (s *Server) deleteOverride(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repository")
	if err := s.Store.DeleteOverride(r.Context(), repo); err != nil {
		s.storeError(w, err)
		return
	}
	s.audit(r, "catalog override removed", "repository", repo)
	s.kickNotify()
	s.listOverrides(w, r)
}

// --- settings & agent manifest ---

type settingsJSON struct {
	ServerURL  string `json:"serverURL"`
	AgentImage string `json:"agentImage"`
}

func (s *Server) loadSettings(r *http.Request) (settingsJSON, error) {
	m, err := s.Store.GetSettings(r.Context())
	return settingsJSON{ServerURL: m[settingServerURL], AgentImage: m[settingAgentImage]}, err
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.loadSettings(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var b settingsJSON
	if !decodeJSON(w, r, &b) {
		return
	}
	b.ServerURL, b.AgentImage = strings.TrimSpace(b.ServerURL), strings.TrimSpace(b.AgentImage)
	if b.ServerURL != "" {
		u, err := url.Parse(b.ServerURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" ||
			u.Fragment != "" || u.User != nil || strings.ContainsAny(b.ServerURL, " \t\n\"'") {
			httpError(w, http.StatusBadRequest, "serverURL: an http(s) URL like https://inventory.example.com")
			return
		}
		b.ServerURL = strings.TrimRight(b.ServerURL, "/")
	}
	if b.AgentImage != "" && !reImage.MatchString(b.AgentImage) {
		httpError(w, http.StatusBadRequest, "agentImage: an image reference like registry.example.com/team/owl:0.7.0")
		return
	}
	for k, v := range map[string]string{settingServerURL: b.ServerURL, settingAgentImage: b.AgentImage} {
		if err := s.Store.SetSetting(r.Context(), k, v); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.audit(r, "settings changed", "serverURL", b.ServerURL, "agentImage", b.AgentImage)
	writeJSON(w, http.StatusOK, b)
}

// agentManifest serves deploy/agent/agent.yaml with the configured image. It contains no secrets.
func (s *Server) agentManifest(w http.ResponseWriter, r *http.Request) {
	st, err := s.loadSettings(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	m := deploy.AgentManifest
	if st.AgentImage != "" { // validated by reImage on save
		m = strings.Replace(m, deploy.AgentImagePlaceholder, "image: "+st.AgentImage, 1)
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="agent.yaml"`)
	w.Write([]byte(m))
}
