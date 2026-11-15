// Package server is the central inventory service: it receives agent snapshots and serves the API.
package server

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"service-inventory/internal/auth"
	"service-inventory/internal/snapshot"
	"service-inventory/internal/store"
)

// Limits protect against oversized or malicious payloads (gzip bombs).
const (
	maxCompressedBody   = 5 << 20  // 5 MiB on the wire
	maxDecompressedBody = 50 << 20 // 50 MiB of JSON (~100k containers)
)

type Server struct {
	Store store.Store
	Log   *slog.Logger
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
	return logRequests(s.Log, mux)
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
	body = io.LimitReader(body, maxDecompressedBody+1)

	var snap snapshot.Snapshot
	dec := json.NewDecoder(body)
	if err := dec.Decode(&snap); err != nil {
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
