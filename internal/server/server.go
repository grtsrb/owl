// Package server is the central inventory service: it receives agent snapshots and serves the API.
package server

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"owl/internal/auth"
	"owl/internal/backup"
	"owl/internal/catalog"
	"owl/internal/inventory"
	"owl/internal/notify"
	"owl/internal/policy"
	"owl/internal/registry"
	"owl/internal/scan"
	"owl/internal/snapshot"
	"owl/internal/store"
	"owl/internal/summary"
	"owl/internal/upstream"
)

// Limits protect against oversized or malicious payloads (gzip bombs).
const (
	maxCompressedBody   = 5 << 20  // 5 MiB on the wire
	maxDecompressedBody = 50 << 20 // 50 MiB of JSON (~100k containers)
)

type Server struct {
	Store store.Store
	// Catalog is the built-in catalog. Requests use cat(): built-in plus the components added in the UI
	// (ReloadCatalog swaps it atomically, so every request sees one consistent catalog).
	Catalog  *catalog.Catalog
	Upstream *upstream.Client // nil = upstream checks disabled
	Log      *slog.Logger

	mu       sync.RWMutex
	upstream map[string]*upstream.Info // component id -> latest info
	// AllowedHosts are the Host header values (without port) the UI and API answer to (defence in
	// depth against DNS rebinding; the public URL's host is added automatically). Empty = localhost
	// only. The agent ingest endpoint is exempt (agents may use an IP; it has its own token auth).
	AllowedHosts []string
	// Auth is the sign-in configuration (public URL, Google, email, trusted proxies).
	Auth   AuthConfig
	limits limiter
	// UI is the built web app (web.Dist()); nil = API only. Docs is the built documentation
	// (docs.Dist()); nil or not built = GET /api/v1/docs answers 404.
	UI   fs.FS
	Docs fs.FS
	docs docsCache
	// Backups is nil when backups are disabled. LogLevel and LogFile (optional) follow the settings
	// edited in the UI (applyOpsSettings).
	Backups *backup.Manager
	// Summaries writes Claude upgrade summaries (nil = upstream checks disabled).
	Summaries *summary.Manager
	// Registry reads OCI labels from public registries for "New component" suggestions (nil = off).
	Registry *registry.Client
	// Scans runs image vulnerability scans (milestone 15); nil = off, ScansOff says why.
	Scans    *scan.Manager
	ScansOff string
	// Notify posts the digest and alerts (milestone 10); nil in tests that don't need it.
	Notify *notify.Notifier
	// CLIDir holds the downloadable client builds (owl-<os>-<arch>.gz + SHA256SUMS); "" or missing =
	// no downloads. Version is the server's version (shown with them).
	CLIDir   string
	Version  string
	cliFiles cliFiles
	LogLevel *slog.LevelVar
	LogFile  interface{ SetRetentionDays(int) }

	fetchKick chan struct{} // kickFetch → RunUpstreamRefresher
	fetchOnce sync.Once

	live    atomic.Pointer[liveCatalog]
	reloadM sync.Mutex // one catalog reload at a time

	uiOnce  sync.Once
	uiIndex []byte
	uiFiles http.Handler
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("POST "+snapshot.IngestPath, s.ingest)
	// Read API: any signed-in user (authn). Writes: admins (requireAdmin), JSON bodies only.
	mux.HandleFunc("GET /api/v1/clusters", s.listClusters)
	mux.HandleFunc("GET /api/v1/clusters/{name}/images", s.clusterImages)
	mux.HandleFunc("GET /api/v1/changes", s.listChanges)
	mux.HandleFunc("GET /api/v1/inventory", s.inventory)
	mux.HandleFunc("GET /api/v1/upstream", s.listUpstream)
	mux.HandleFunc("GET /api/v1/components/{id}", s.componentDetail)
	mux.HandleFunc("GET /api/v1/report", s.report)
	mux.HandleFunc("GET /api/v1/policies", s.listPolicies)
	mux.HandleFunc("PUT /api/v1/policies/{scope}", s.requireAdmin(s.setPolicy))
	mux.HandleFunc("DELETE /api/v1/policies/{scope}", s.requireAdmin(s.deletePolicy))
	mux.HandleFunc("GET /api/v1/catalog", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, s.cat()) })
	mux.HandleFunc("GET /api/v1/catalog/overrides", s.listOverrides)
	mux.HandleFunc("GET /api/v1/stages", s.listStages)
	mux.HandleFunc("GET /api/v1/tenants", s.listTenants)
	mux.HandleFunc("GET /api/v1/settings", s.getSettings)
	mux.HandleFunc("GET /api/v1/agent/manifest", s.agentManifest)
	// Admin: cluster management, stages, tenants, overrides, settings. JSON-only bodies (decodeJSON).
	mux.HandleFunc("POST /api/v1/clusters", s.requireAdmin(s.createCluster))
	mux.HandleFunc("PATCH /api/v1/clusters/{name}", s.requireAdmin(s.updateCluster))
	mux.HandleFunc("DELETE /api/v1/clusters/{name}", s.requireAdmin(s.deleteCluster))
	mux.HandleFunc("POST /api/v1/clusters/{name}/rotate-token", s.requireAdmin(s.rotateToken))
	mux.HandleFunc("GET /api/v1/clusters/{name}/export", s.requireAdmin(s.exportCluster))
	mux.HandleFunc("POST /api/v1/stages", s.requireAdmin(s.createStage))
	mux.HandleFunc("PUT /api/v1/stages", s.requireAdmin(s.reorderStages))
	mux.HandleFunc("DELETE /api/v1/stages/{name}", s.requireAdmin(s.deleteStage))
	mux.HandleFunc("POST /api/v1/tenants", s.requireAdmin(s.createTenant))
	mux.HandleFunc("PATCH /api/v1/tenants/{name}", s.requireAdmin(s.renameTenant))
	mux.HandleFunc("DELETE /api/v1/tenants/{name}", s.requireAdmin(s.deleteTenant))
	mux.HandleFunc("PUT /api/v1/catalog/overrides", s.requireAdmin(s.setOverride))
	mux.HandleFunc("DELETE /api/v1/catalog/overrides", s.requireAdmin(s.deleteOverride))
	mux.HandleFunc("PUT /api/v1/settings", s.requireAdmin(s.putSettings))
	s.opsRoutes(mux)
	s.summaryRoutes(mux)
	s.notifyRoutes(mux)
	s.catalogRoutes(mux)
	s.signinRoutes(mux)
	s.tokenRoutes(mux)
	s.vulnRoutes(mux)
	mux.HandleFunc("GET /api/v1/docs", s.listDocs)
	mux.HandleFunc("GET /api/v1/docs/{page}", s.docPage)
	mux.HandleFunc("GET /api/v1/cli", s.listCLI)
	mux.HandleFunc("GET /api/v1/cli/{platform}", s.downloadCLI)
	mux.HandleFunc("/", s.fallback) // UI for GET/HEAD, JSON 404 for everything else
	return logRequests(s.Log, s.clientIP, securityHeaders(s.checkHost(s.authn(mux))))
}

// securityHeaders: the UI loads only same-origin scripts/styles (Vite emits no inline code), can't be
// framed, and responses aren't MIME-sniffed.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; object-src 'none'; "+
			"base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// fallback handles every request no API route matched: anything under /api gets a JSON 404 (any
// method); other GET/HEAD requests get the UI.
func (s *Server) fallback(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") ||
		(r.Method != http.MethodGet && r.Method != http.MethodHead) {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	s.serveUI(w, r)
}

const uiMissing = "UI not built: run `make ui` (the API is at /api/v1/...)"

// serveUI serves built assets (real 404s for missing ones, so a stale tab after a deploy fails
// visibly) and index.html for client-side routes (/components/x).
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if s.UI == nil {
		http.Error(w, uiMissing, http.StatusNotFound)
		return
	}
	s.uiOnce.Do(func() {
		s.uiIndex, _ = fs.ReadFile(s.UI, "index.html")
		s.uiFiles = http.FileServerFS(s.UI)
	})
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // content-hashed names
		s.uiFiles.ServeHTTP(w, r)                                              // 404s on its own
		return
	}
	if name != "" && name != "index.html" {
		if st, err := fs.Stat(s.UI, name); err == nil && !st.IsDir() { // e.g. favicon
			s.uiFiles.ServeHTTP(w, r)
			return
		}
	}
	if s.uiIndex == nil {
		http.Error(w, uiMissing, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(s.uiIndex)
}

func (s *Server) checkHost(next http.Handler) http.Handler {
	allowed := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	extra := s.AllowedHosts
	if s.Auth.PublicURL != nil {
		extra = append(append([]string{}, extra...), s.Auth.PublicURL.Host)
	}
	for _, h := range extra {
		h = strings.TrimSpace(strings.ToLower(h))
		if hostOnly, _, err := net.SplitHostPort(h); err == nil {
			h = hostOnly // "inv.internal:8080" -> "inv.internal" (the request side is compared without port)
		}
		if h = strings.Trim(h, "[]"); h != "" {
			allowed[h] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == snapshot.IngestPath || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !allowed[strings.ToLower(strings.Trim(host, "[]"))] {
			httpError(w, http.StatusMisdirectedRequest, "host not allowed (set OWL_ALLOWED_HOSTS)")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.BearerToken(r.Header.Get("Authorization"))
	if !ok {
		httpError(w, http.StatusUnauthorized, "missing or malformed bearer token")
		return
	}
	cluster, err := s.Store.ClusterByTokenHash(r.Context(), auth.Hash(token))
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusUnauthorized, "invalid or revoked token")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}

	var body io.Reader = http.MaxBytesReader(w, r.Body, maxCompressedBody)
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(body)
		if err != nil {
			httpError(w, http.StatusBadRequest, "invalid gzip body")
			return
		}
		defer zr.Close()
		body = zr
	}
	limited := &limitReader{r: body, n: maxDecompressedBody}

	var snap snapshot.Snapshot
	dec := json.NewDecoder(limited)
	err = dec.Decode(&snap)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("unexpected data after snapshot")
	}
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxErr) || errors.Is(err, errTooLarge):
		httpError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf(
			"snapshot too large (limits: %d MiB compressed, %d MiB decompressed)", maxCompressedBody>>20, maxDecompressedBody>>20))
		return
	case err != nil:
		httpError(w, http.StatusBadRequest, "invalid snapshot: "+err.Error())
		return
	}
	if snap.SchemaVersion != snapshot.SchemaVersion {
		httpError(w, http.StatusBadRequest, fmt.Sprintf("unsupported schemaVersion %d (server expects %d)",
			snap.SchemaVersion, snapshot.SchemaVersion))
		return
	}

	res, err := s.Store.Ingest(r.Context(), cluster.ID, &snap)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("snapshot ingested", "cluster", cluster.Name, "workloads", len(snap.Workloads),
		"changes", len(res.Changes), "first", res.First, "agent", snap.AgentVersion)
	if len(res.Changes) > 0 || res.First {
		s.kickFetch()     // a component that just started running gets its upstream data now
		s.kickSummaries() // running versions changed: the upgrades needed may have too
		s.kickNotify()
	}
	// Every push: a new digest often arrives without a template change (pods restarted after the change was
	// already reported, or a re-pushed mutable tag). The sync is cheap and idempotent.
	s.kickScans()
	writeJSON(w, http.StatusAccepted, map[string]any{"cluster": cluster.Name, "changes": len(res.Changes)})
}

var errTooLarge = errors.New("decompressed body too large")

// limitReader is like io.LimitReader but returns errTooLarge instead of a silent EOF when exceeded.
type limitReader struct {
	r io.Reader
	n int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		// At the limit: only an error if there is actually more data.
		var one [1]byte
		if n, err := l.r.Read(one[:]); n > 0 {
			return 0, errTooLarge
		} else {
			return 0, err
		}
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

type clusterJSON struct {
	Name              string     `json:"name"`
	Stage             string     `json:"stage"`
	Tenant            string     `json:"tenant"`
	KubernetesVersion string     `json:"kubernetesVersion"`
	AgentVersion      string     `json:"agentVersion"`
	LastSeenAt        *time.Time `json:"lastSeenAt"`
}

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Store.ListClusters(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := []clusterJSON{}
	for i := range cs {
		out = append(out, toClusterJSON(&cs[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) clusterImages(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.GetCluster(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "cluster not found")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	rows, err := s.Store.CurrentImages(r.Context(), c.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if rows == nil {
		rows = []store.ImageRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) listChanges(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	cs, err := s.Store.ListChanges(r.Context(), r.URL.Query().Get("cluster"), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	if cs == nil {
		cs = []store.Change{}
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) inventory(w http.ResponseWriter, r *http.Request) {
	cat := s.cat()
	inv, err := inventory.Build(r.Context(), s.Store, cat)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.Log.Error("request failed", "err", err)
	httpError(w, http.StatusInternalServerError, "internal error")
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) { r.code = code; r.ResponseWriter.WriteHeader(code) }

type ctxKey int

const requestIDKey ctxKey = 1

// requestID returns the id logRequests gave this request ("" outside a request).
func requestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey).(string)
	return id
}

// logRequests logs one line per request with a request id (also sent as X-Request-ID, so a user can
// quote it). Only method, path, status, duration and remote are logged — never headers (Authorization)
// or query strings. Health checks and static assets log at debug.
func logRequests(log *slog.Logger, clientIP func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		var b [6]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		w.Header().Set("X-Request-ID", id)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, id))
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		next.ServeHTTP(rec, r)
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/assets/") || !strings.HasPrefix(r.URL.Path, "/api/") && r.Method == http.MethodGet {
			level = slog.LevelDebug
		}
		log.Log(r.Context(), level, "http", "method", r.Method, "path", r.URL.Path, "status", rec.code,
			"took", time.Since(start).Round(time.Millisecond), "remote", clientIP(r), "req", id)
	})
}

// runningComponents: the components upstream data is kept for — every one running in at least one
// cluster (identified or unpinned, through the catalog or a map override, and the cluster-sourced ones)
// plus every custom component (someone added it on purpose). A large built-in catalog costs nothing
// until a cluster runs a component of it.
func (s *Server) runningComponents(ctx context.Context, cat *catalog.Catalog) (map[string]bool, error) {
	inv, err := inventory.Build(ctx, s.Store, cat)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, cv := range inv.Components {
		out[cv.ID] = true
	}
	for _, c := range cat.Components {
		if c.Custom {
			out[c.ID] = true
		}
	}
	return out, nil
}

// RefreshUpstream fetches upstream info for the running components (cached responses make this cheap;
// only expired entries hit the network). Components that stopped running anywhere leave the map; their
// cache rows stay, so coming back costs one revalidation.
func (s *Server) RefreshUpstream(ctx context.Context) {
	if s.Upstream == nil {
		return
	}
	start := time.Now()
	fresh := map[string]*upstream.Info{}
	errs := 0
	cat := s.cat()
	want, err := s.runningComponents(ctx, cat)
	if err != nil {
		s.Log.Error("upstream refresh: listing running components failed; refreshing all", "err", err)
	}
	for i := range cat.Components {
		c := &cat.Components[i]
		if want != nil && !want[c.ID] {
			continue
		}
		info := s.Upstream.Refresh(ctx, c)
		if len(info.Errors) > 0 {
			errs++
			s.Log.Warn("upstream refresh incomplete", "component", c.ID, "errors", info.Errors)
		}
		fresh[c.ID] = info
	}
	// The catalog or the clusters may have changed while this ran (a component added in the UI, a push):
	// keep what was fetched meanwhile for components this refresh didn't cover and that are still wanted,
	// drop the rest.
	now := s.cat()
	still, err := s.runningComponents(ctx, now)
	if err != nil {
		still = nil
	}
	s.mu.Lock()
	for id, info := range s.upstream {
		if _, ok := fresh[id]; !ok && now.ByID(id) != nil && (still == nil || still[id]) {
			fresh[id] = info
		}
	}
	for id := range fresh {
		if now.ByID(id) == nil {
			delete(fresh, id)
		}
	}
	s.upstream = fresh
	s.mu.Unlock()
	s.Log.Info("upstream refreshed", "components", len(fresh), "catalog", len(now.Components), "withErrors", errs,
		"took", time.Since(start).Round(time.Millisecond))
	s.kickSummaries() // new targets or releases: jumps and notes may have changed
	s.kickNotify()    // new advisories or a passed EOL date change levels without any cluster change
}

// fetchKicks: pushes (and map overrides) ask the refresher to fetch components that started running.
func (s *Server) fetchKicks() chan struct{} {
	s.fetchOnce.Do(func() { s.fetchKick = make(chan struct{}, 1) })
	return s.fetchKick
}

// kickFetch asks for upstream data of components that run now but have none yet (a new deployment),
// so they don't wait for the next scheduled refresh.
func (s *Server) kickFetch() {
	select {
	case s.fetchKicks() <- struct{}{}:
	default:
	}
}

// FetchNew fetches upstream data for running components that have none yet. Before the first refresh it
// does nothing (that refresh covers them).
func (s *Server) FetchNew(ctx context.Context) {
	if s.Upstream == nil {
		return
	}
	cat := s.cat()
	want, err := s.runningComponents(ctx, cat)
	if err != nil {
		s.Log.Error("fetching new components failed", "err", err)
		return
	}
	s.mu.RLock()
	loaded := s.upstream != nil
	var missing []*catalog.Component
	for i := range cat.Components {
		c := &cat.Components[i]
		if _, ok := s.upstream[c.ID]; want[c.ID] && !ok {
			missing = append(missing, c)
		}
	}
	s.mu.RUnlock()
	if !loaded || len(missing) == 0 {
		return
	}
	got := map[string]*upstream.Info{}
	for _, c := range missing {
		got[c.ID] = s.Upstream.Refresh(ctx, c)
	}
	s.mu.Lock()
	next := make(map[string]*upstream.Info, len(s.upstream)+len(got))
	for k, v := range s.upstream {
		next[k] = v
	}
	now := s.cat()
	for id, info := range got {
		if _, ok := next[id]; !ok && now.ByID(id) != nil {
			next[id] = info
		}
	}
	s.upstream = next
	s.mu.Unlock()
	ids := make([]string, 0, len(got))
	for id := range got {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	s.Log.Info("upstream fetched for components that started running", "components", ids)
	s.kickSummaries()
	s.kickNotify()
}

// RunUpstreamRefresher refreshes now and then every interval until ctx is done; in between it fetches
// components that started running (kickFetch). One goroutine does all of it, so they never overlap.
func (s *Server) RunUpstreamRefresher(ctx context.Context, every time.Duration) {
	s.RefreshUpstream(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RefreshUpstream(ctx)
		case <-s.fetchKicks():
			s.FetchNew(ctx)
		}
	}
}

func (s *Server) upstreamInfo(id string) *upstream.Info {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.upstream[id]
}

func (s *Server) listUpstream(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	out := make([]*upstream.Info, 0, len(s.upstream))
	for _, i := range s.upstream {
		out = append(out, i)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ComponentID < out[j].ComponentID })
	writeJSON(w, http.StatusOK, out)
}

type versionAdvisories struct {
	Version    string           `json:"version"`
	Advisories []upstream.Match `json:"advisories"`
	Error      string           `json:"error,omitempty"`
}

// componentDetail: where the component runs, upstream info, and advisories per running version.
func (s *Server) componentDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cat := s.cat()
	comp := cat.ByID(id)
	if comp == nil {
		httpError(w, http.StatusNotFound, "unknown component")
		return
	}
	inv, err := inventory.Build(r.Context(), s.Store, cat)
	if err != nil {
		s.fail(w, err)
		return
	}
	var view *inventory.ComponentView
	for i := range inv.Components {
		if inv.Components[i].ID == id {
			view = &inv.Components[i]
		}
	}
	info := s.upstreamInfo(id)
	advs := []versionAdvisories{}
	if view != nil && s.Upstream != nil {
		seen := map[string]bool{}
		for _, in := range view.Instances {
			if in.Version == "" || seen[in.Version] {
				continue
			}
			seen[in.Version] = true
			va := versionAdvisories{Version: in.Version, Advisories: []upstream.Match{}}
			switch {
			case in.Status == string(catalog.StatusUnpinned):
				// "redis:7" is some 7.x — matching it as 7.0.0 would invent vulnerabilities.
				va.Error = "floating/unpinned tag: exact version unknown, advisories not evaluated"
			case info == nil:
				va.Error = "upstream data not loaded yet"
			default:
				va.Advisories = upstream.AdvisoriesFor(info, in.Version)
			}
			if va.Advisories == nil {
				va.Advisories = []upstream.Match{}
			}
			advs = append(advs, va)
		}
	}
	scans, confirmed, err := s.componentScans(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"component": comp, "inventory": view, "upstream": info, "advisoriesByVersion": advs,
		"imageScans": scans, "trivyConfirmed": confirmed,
	})
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	rep, err := s.BuildReport(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	pols, err := s.Store.ListPolicies(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	global := pols["global"]
	delete(pols, "global")
	if global == "" {
		global = policy.Default
	}
	writeJSON(w, http.StatusOK, map[string]any{"global": global, "overrides": pols, "presets": policy.Presets})
}

func (s *Server) setPolicy(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	scope := r.PathValue("scope")
	if scope != "global" && s.cat().ByID(scope) == nil {
		httpError(w, http.StatusNotFound, "unknown component (use a component id or \"global\")")
		return
	}
	var body struct {
		Policy string `json:"policy"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	sp, err := policy.Parse(body.Policy)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if scope == "global" && sp.Kind == "pin" {
		// One version for every component (Redis, CoreDNS, Postgres…) makes every target meaningless.
		httpError(w, http.StatusBadRequest, "a version pin is per component: set it on the component's page, not as the global policy")
		return
	}
	if err := s.Store.SetPolicy(r.Context(), scope, sp.Raw); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "policy set", "scope", scope, "policy", sp.Raw)
	s.kickNotify()
	writeJSON(w, http.StatusOK, map[string]string{"scope": scope, "policy": sp.Raw})
}

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request) {
	scope := r.PathValue("scope")
	if err := s.Store.DeletePolicy(r.Context(), scope); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "policy removed", "scope", scope)
	s.kickNotify()
	writeJSON(w, http.StatusOK, map[string]string{"scope": scope, "policy": "inherited"})
}
