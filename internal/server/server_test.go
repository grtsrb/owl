package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"service-inventory/internal/auth"
	"service-inventory/internal/catalog"
	"service-inventory/internal/snapshot"
	"service-inventory/internal/store"
	"service-inventory/internal/store/sqlite"
)

const testAdminToken = "test-admin-token-0123456789abcdef"

func setup(t *testing.T) (*httptest.Server, *sqlite.DB, string) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c, err := db.CreateCluster(context.Background(), "it-hetzner", store.StageStaging, "IT")
	if err != nil {
		t.Fatal(err)
	}
	token, hash, prefix := auth.NewToken()
	if err := db.AddToken(context.Background(), c.ID, hash, prefix); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&Server{Store: db, Catalog: cat, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		AdminToken: testAdminToken}).Handler())
	t.Cleanup(srv.Close)
	return srv, db, token
}

func snap(k8s string, images map[string]string) *snapshot.Snapshot {
	s := &snapshot.Snapshot{SchemaVersion: snapshot.SchemaVersion, AgentVersion: "test",
		Cluster: snapshot.Cluster{KubernetesVersion: k8s}}
	for name, img := range images {
		s.Workloads = append(s.Workloads, snapshot.Workload{Kind: "Deployment", Namespace: "seed", Name: name,
			DesiredReplicas: 1, Containers: []snapshot.Container{{Name: name, Image: img}}})
	}
	return s
}

func push(t *testing.T, url, token string, s *snapshot.Snapshot) int {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_ = json.NewEncoder(zw).Encode(s)
	zw.Close()
	req, _ := http.NewRequest(http.MethodPost, url+snapshot.IngestPath, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestIngestAndHistory(t *testing.T) {
	srv, db, token := setup(t)
	ctx := context.Background()

	if code := push(t, srv.URL, token, snap("v1.30.4+rke2r1", map[string]string{
		"web": "nginx:1.25.4", "cache": "redis:7.2.4", "old": "busybox:1.36"})); code != http.StatusAccepted {
		t.Fatalf("first push: %d", code)
	}
	if code := push(t, srv.URL, token, snap("v1.31.2+rke2r1", map[string]string{
		"web": "nginx:1.27.3", "cache": "redis:7.2.4", "db": "postgres:16.4"})); code != http.StatusAccepted {
		t.Fatalf("second push: %d", code)
	}

	changes, err := db.ListChanges(ctx, "it-hetzner", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range changes {
		got[string(c.Type)+":"+c.Workload] = c.OldValue + "->" + c.NewValue
	}
	want := map[string]string{
		"first_snapshot:":             "->3 containers", // initial inventory is one event, not 3 "added"
		"image_changed:web":           "nginx:1.25.4->nginx:1.27.3",
		"added:db":                    "->postgres:16.4",
		"removed:old":                 "busybox:1.36->",
		"kubernetes_version_changed:": "v1.30.4+rke2r1->v1.31.2+rke2r1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("change %s: got %q want %q (all: %v)", k, got[k], v, got)
		}
	}
	if len(changes) != len(want) {
		t.Errorf("want %d changes, got %d: %v", len(want), len(changes), got)
	}

	c, _ := db.GetCluster(ctx, "it-hetzner")
	if c.KubernetesVersion != "v1.31.2+rke2r1" || c.LastSeenAt == nil {
		t.Errorf("cluster not updated: %+v", c)
	}
	rows, _ := db.CurrentImages(ctx, c.ID)
	if len(rows) != 3 {
		t.Errorf("want 3 current images, got %d", len(rows))
	}
}

func TestIngestRejectsBadAuthAndPayloads(t *testing.T) {
	srv, db, token := setup(t)
	s := snap("v1", map[string]string{"web": "nginx:1"})

	if code := push(t, srv.URL, "inv_not-a-real-token", s); code != http.StatusUnauthorized {
		t.Errorf("unknown token: got %d", code)
	}
	if code := push(t, srv.URL, "garbage", s); code != http.StatusUnauthorized {
		t.Errorf("malformed token: got %d", code)
	}
	bad := *s
	bad.SchemaVersion = 99
	if code := push(t, srv.URL, token, &bad); code != http.StatusBadRequest {
		t.Errorf("wrong schema: got %d", code)
	}

	// Revoked tokens stop working.
	c, _ := db.GetCluster(context.Background(), "it-hetzner")
	_ = db.RevokeTokens(context.Background(), c.ID)
	if code := push(t, srv.URL, token, s); code != http.StatusUnauthorized {
		t.Errorf("revoked token: got %d", code)
	}
}

func TestOversizedPayloadRejected(t *testing.T) {
	srv, _, token := setup(t)
	// Valid-looking JSON (one huge string), so the parser keeps reading until the limit trips.
	big := append([]byte(`{"workloads":"`), bytes.Repeat([]byte("a"), maxCompressedBody+1)...)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+snapshot.IngestPath, bytes.NewReader(big))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: got %d", resp.StatusCode)
	}
}

func TestReadAPIRejectsForeignHost(t *testing.T) {
	srv, _, _ := setup(t)
	get := func(host string) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/clusters", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("evil.example.com"); code != http.StatusMisdirectedRequest {
		t.Errorf("rebinding host: got %d", code)
	}
	if code := get("127.0.0.1:8080"); code != http.StatusOK {
		t.Errorf("localhost: got %d", code)
	}
}

func TestLimitReaderAcceptsExactLimit(t *testing.T) {
	exact := &limitReader{r: bytes.NewReader(make([]byte, 10)), n: 10}
	if b, err := io.ReadAll(exact); err != nil || len(b) != 10 {
		t.Errorf("exact size: %d bytes, err %v", len(b), err)
	}
	over := &limitReader{r: bytes.NewReader(make([]byte, 11)), n: 10}
	if _, err := io.ReadAll(over); !errors.Is(err, errTooLarge) {
		t.Errorf("over limit: want errTooLarge, got %v", err)
	}
}

func TestAllowedHostWithPort(t *testing.T) {
	h := (&Server{AllowedHosts: []string{"inv.internal:8080"}}).checkHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	req.Host = "inv.internal:8080"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("allowed host with port: got %d", rec.Code)
	}
}

func TestPolicyAPI(t *testing.T) {
	srv, _, _ := setup(t)
	put := func(ct, body string) int {
		return putAs(t, srv.URL, testAdminToken, ct, body)
	}
	if c := putAs(t, srv.URL, "", "application/json", `{"policy":"latest"}`); c != http.StatusUnauthorized {
		t.Errorf("no admin token must be refused: %d", c)
	}
	if c := putAs(t, srv.URL, "wrong-token-wrong-token-wrong", "application/json", `{"policy":"latest"}`); c != http.StatusUnauthorized {
		t.Errorf("wrong admin token must be refused: %d", c)
	}
	if c := put("application/x-www-form-urlencoded", "policy=latest"); c != http.StatusUnsupportedMediaType {
		t.Errorf("form post must be refused (CSRF): %d", c)
	}
	if c := put("application/json", `{"policy":"bogus"}`); c != http.StatusBadRequest {
		t.Errorf("invalid policy: %d", c)
	}
	if c := put("application/json", `{"policy":"pin:12.3"}`); c != http.StatusOK {
		t.Errorf("valid policy: %d", c)
	}
}

func putAs(t *testing.T, base, token, ct, body string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, base+"/api/v1/policies/grafana", strings.NewReader(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestWritesDisabledWithoutAdminToken(t *testing.T) {
	h := (&Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).requireAdmin(func(w http.ResponseWriter, r *http.Request) {})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policies/x", nil)
	req.Header.Set("Authorization", "Bearer ")
	h(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("no configured token must disable writes: %d", rec.Code)
	}
}

func TestFallbackRouting(t *testing.T) {
	srv, _, _ := setup(t)
	do := func(method, path string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Content-Type")
	}
	for _, c := range []struct{ method, path string }{{"GET", "/api"}, {"GET", "/api/v1/nope"}, {"POST", "/api/v1/nope"}, {"PUT", "/api/v1/clusters"}, {"DELETE", "/whatever"}} {
		if code, ct := do(c.method, c.path); code != http.StatusNotFound || !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s %s: %d %s", c.method, c.path, code, ct)
		}
	}
}

func TestMissingAssetIs404(t *testing.T) {
	ui := fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}, "assets/app-1.js": {Data: []byte("x")}}
	h := (&Server{UI: ui, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	get := func(p string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Host = "localhost"
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := get("/assets/app-OLD.js"); c != http.StatusNotFound {
		t.Errorf("missing asset: %d", c)
	}
	if c := get("/assets/app-1.js"); c != http.StatusOK {
		t.Errorf("asset: %d", c)
	}
	if c := get("/components/redis"); c != http.StatusOK {
		t.Errorf("client route: %d", c)
	}
}
