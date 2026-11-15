// Package server is the central inventory service: it receives agent snapshots and serves the API.
package server

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"service-inventory/internal/auth"
	"service-inventory/internal/catalog"
	"service-inventory/internal/inventory"
	"service-inventory/internal/policy"
	"service-inventory/internal/report"
	"service-inventory/internal/snapshot"
	"service-inventory/internal/store"
	"service-inventory/internal/upstream"
)

// Limits protect against oversized or malicious payloads (gzip bombs).
const (
	maxCompressedBody   = 5 << 20  // 5 MiB on the wire
	maxDecompressedBody = 50 << 20 // 50 MiB of JSON (~100k containers)
)

type Server struct {
	Store    store.Store
	Catalog  *catalog.Catalog
	Upstream *upstream.Client // nil = upstream checks disabled
	Log      *slog.Logger

	mu       sync.RWMutex
	upstream map[string]*upstream.Info // component id -> latest info
	// AllowedHosts are the Host header values (without port) the read API answers to. It blocks DNS
	// rebinding against the unauthenticated read API until admin auth exists. Empty = localhost only.
	// The agent ingest endpoint is exempt (agents may use an IP; it has its own token auth).
	AllowedHosts []string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("POST "+snapshot.IngestPath, s.ingest)
	// Read API. NOTE: unauthenticated until admin auth lands (milestone 8) — bind to localhost or keep
	// behind a private network until then.
	mux.HandleFunc("GET /api/v1/clusters", s.listClusters)
	mux.HandleFunc("GET /api/v1/clusters/{name}/images", s.clusterImages)
	mux.HandleFunc("GET /api/v1/changes", s.listChanges)
	mux.HandleFunc("GET /api/v1/inventory", s.inventory)
	mux.HandleFunc("GET /api/v1/upstream", s.listUpstream)
	mux.HandleFunc("GET /api/v1/components/{id}", s.componentDetail)
	mux.HandleFunc("GET /api/v1/report", s.report)
	mux.HandleFunc("GET /api/v1/policies", s.listPolicies)
	// Policy writes: JSON only. Together with no CORS headers this means a cross-site page can't
	// change policies (a JSON PUT from another origin needs a preflight we never answer).
	mux.HandleFunc("PUT /api/v1/policies/{scope}", s.setPolicy)
	mux.HandleFunc("DELETE /api/v1/policies/{scope}", s.deletePolicy)
	mux.HandleFunc("GET /api/v1/catalog", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, s.Catalog) })
	return logRequests(s.Log, s.checkHost(mux))
}

func (s *Server) checkHost(next http.Handler) http.Handler {
	allowed := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	for _, h := range s.AllowedHosts {
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
			httpError(w, http.StatusMisdirectedRequest, "host not allowed (set INV_ALLOWED_HOSTS)")
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
	for _, c := range cs {
		out = append(out, clusterJSON{c.Name, string(c.Stage), c.Tenant, c.KubernetesVersion, c.AgentVersion, c.LastSeenAt})
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
	inv, err := inventory.Build(r.Context(), s.Store, s.Catalog)
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

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" {
			return
		}
		log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.code,
			"took", time.Since(start).Round(time.Millisecond), "remote", r.RemoteAddr)
	})
}

// RefreshUpstream fetches upstream info for every catalog component (cached responses make this
// cheap; only expired entries hit the network).
func (s *Server) RefreshUpstream(ctx context.Context) {
	if s.Upstream == nil {
		return
	}
	start := time.Now()
	fresh := map[string]*upstream.Info{}
	errs := 0
	for i := range s.Catalog.Components {
		c := &s.Catalog.Components[i]
		info := s.Upstream.Refresh(ctx, c)
		if len(info.Errors) > 0 {
			errs++
			s.Log.Warn("upstream refresh incomplete", "component", c.ID, "errors", info.Errors)
		}
		fresh[c.ID] = info
	}
	s.mu.Lock()
	s.upstream = fresh
	s.mu.Unlock()
	s.Log.Info("upstream refreshed", "components", len(fresh), "withErrors", errs, "took", time.Since(start).Round(time.Millisecond))
}

// RunUpstreamRefresher refreshes now and then every interval until ctx is done.
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
	comp := s.Catalog.ByID(id)
	if comp == nil {
		httpError(w, http.StatusNotFound, "unknown component")
		return
	}
	inv, err := inventory.Build(r.Context(), s.Store, s.Catalog)
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
				ms, err := s.Upstream.AdvisoriesFor(r.Context(), comp, info, in.Version)
				va.Advisories = ms
				if err != nil {
					va.Error = err.Error()
				}
			}
			if va.Advisories == nil {
				va.Advisories = []upstream.Match{}
			}
			advs = append(advs, va)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"component": comp, "inventory": view, "upstream": info, "advisoriesByVersion": advs,
	})
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	rep, err := report.Build(r.Context(), report.Inputs{
		Catalog: s.Catalog, Store: s.Store, Upstream: s.Upstream, Infos: s.upstreamInfo, Now: time.Now().UTC(),
	})
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
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		httpError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	scope := r.PathValue("scope")
	if scope != "global" && s.Catalog.ByID(scope) == nil {
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
	if err := s.Store.SetPolicy(r.Context(), scope, sp.Raw); err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("policy changed", "scope", scope, "policy", sp.Raw)
	writeJSON(w, http.StatusOK, map[string]string{"scope": scope, "policy": sp.Raw})
}

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request) {
	scope := r.PathValue("scope")
	if err := s.Store.DeletePolicy(r.Context(), scope); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"scope": scope, "policy": "inherited"})
}
