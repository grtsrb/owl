package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func call(t *testing.T, method, url, body string, admin bool) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if admin {
		req.Header.Set("Authorization", "Bearer "+testAdminToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{"raw": string(raw)}
	}
	return resp.StatusCode, out
}

func TestClusterManagement(t *testing.T) {
	srv, db, oldToken := setup(t)
	u := srv.URL + "/api/v1"

	if code, _ := call(t, "POST", u+"/clusters", `{"name":"ds-prod","stage":"live","tenant":"DS"}`, false); code != 401 {
		t.Errorf("create without admin token: %d", code)
	}
	if code, _ := call(t, "POST", u+"/clusters", `{"name":"DS Prod","stage":"live","tenant":"DS"}`, true); code != 400 {
		t.Errorf("invalid name: %d", code)
	}
	if code, _ := call(t, "POST", u+"/clusters", `{"name":"x","stage":"qa","tenant":"DS"}`, true); code != 400 {
		t.Errorf("unknown stage: %d", code)
	}
	code, out := call(t, "POST", u+"/clusters", `{"name":"ds-prod","stage":"live","tenant":"DS"}`, true)
	token, _ := out["token"].(string)
	if code != 201 || token == "" {
		t.Fatalf("create: %d %v", code, out)
	}
	if code, _ := call(t, "POST", u+"/clusters", `{"name":"ds-prod","stage":"live","tenant":"DS"}`, true); code != 409 {
		t.Errorf("duplicate: %d", code)
	}
	if got := push(t, srv.URL, token, snap("v1.33.6+k3s1", map[string]string{"web": "nginx:1.25.4"})); got != 202 {
		t.Errorf("new token must ingest: %d", got)
	}

	// Edit: stage and tenant must exist; tenant DS was created along with the cluster.
	if code, _ := call(t, "PATCH", u+"/clusters/ds-prod", `{"stage":"prelive","tenant":"nope"}`, true); code != 400 {
		t.Errorf("edit to unknown tenant: %d", code)
	}
	if code, out := call(t, "PATCH", u+"/clusters/ds-prod", `{"stage":"prelive","tenant":"DS"}`, true); code != 200 || out["stage"] != "prelive" {
		t.Errorf("edit: %d %v", code, out)
	}

	// Rotation: the old token stops working at once, the new one works.
	code, out = call(t, "POST", u+"/clusters/it-hetzner/rotate-token", "", true)
	newToken, _ := out["token"].(string)
	if code != 200 || newToken == "" {
		t.Fatalf("rotate: %d %v", code, out)
	}
	if got := push(t, srv.URL, oldToken, snap("v1.36.4+k3s1", nil)); got != 401 {
		t.Errorf("old token after rotation: %d", got)
	}
	if got := push(t, srv.URL, newToken, snap("v1.36.4+k3s1", nil)); got != 202 {
		t.Errorf("new token after rotation: %d", got)
	}

	// Export is admin-only and carries history but no token hashes.
	if code, _ := call(t, "GET", u+"/clusters/ds-prod/export", "", false); code != 401 {
		t.Errorf("export without admin: %d", code)
	}
	code, out = call(t, "GET", u+"/clusters/ds-prod/export", "", true)
	if code != 200 || out["format"] != "service-inventory/cluster-export" || len(out["images"].([]any)) != 1 || len(out["changes"].([]any)) != 1 {
		t.Errorf("export: %d %v", code, out)
	}
	if strings.Contains(fmt.Sprint(out), "token") {
		t.Error("export must not contain token data")
	}

	// Stage / tenant in use can't be deleted.
	if code, _ := call(t, "DELETE", u+"/stages/prelive", "", true); code != 409 {
		t.Errorf("delete stage in use: %d", code)
	}
	if code, _ := call(t, "DELETE", u+"/tenants/DS", "", true); code != 409 {
		t.Errorf("delete tenant in use: %d", code)
	}
	if code, _ := call(t, "DELETE", u+"/clusters/ds-prod", "", true); code != 200 {
		t.Errorf("delete cluster: %d", code)
	}
	if _, err := db.GetCluster(context.Background(), "ds-prod"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cluster still there: %v", err)
	}
	if code, _ := call(t, "DELETE", u+"/tenants/DS", "", true); code != 200 {
		t.Errorf("delete unused tenant: %d", code)
	}
}

func TestSettingsAndManifest(t *testing.T) {
	srv, _, _ := setup(t)
	u := srv.URL + "/api/v1"
	// Anything that could break out of the YAML line is rejected.
	for _, img := range []string{"evil\n  hostNetwork: true", "a b", `x"y`, "img:1 # c"} {
		body, _ := json.Marshal(map[string]string{"agentImage": img})
		if code, _ := call(t, "PUT", u+"/settings", string(body), true); code != 400 {
			t.Errorf("image %q accepted: %d", img, code)
		}
	}
	for _, su := range []string{"ftp://x", "https://", "https://u:p@x", "https://x/?a=b", "not a url"} {
		body, _ := json.Marshal(map[string]string{"serverURL": su})
		if code, _ := call(t, "PUT", u+"/settings", string(body), true); code != 400 {
			t.Errorf("server URL %q accepted: %d", su, code)
		}
	}
	req, _ := http.NewRequest("PUT", u+"/settings", strings.NewReader(`agentImage=x`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 415 {
		t.Errorf("form posts must be refused: %v %v", resp.StatusCode, err)
	}

	code, out := call(t, "PUT", u+"/settings", `{"serverURL":"https://inv.example.com/","agentImage":"registry.example.com/devops/service-inventory:0.7.0"}`, true)
	if code != 200 || out["serverURL"] != "https://inv.example.com" {
		t.Fatalf("settings: %d %v", code, out)
	}
	resp, err := http.Get(u + "/agent/manifest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	m, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(m), "image: registry.example.com/devops/service-inventory:0.7.0\n") ||
		strings.Contains(string(m), "replace with your registry image") {
		t.Errorf("manifest image not substituted:\n%s", m)
	}
	if strings.Contains(string(m), "kind: Secret\n") {
		t.Error("the manifest must not contain the token Secret")
	}
	for _, line := range strings.Split(string(m), "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "resources:") && strings.Contains(l, "secrets") ||
			strings.HasPrefix(l, "verbs:") && strings.ContainsAny(strings.TrimPrefix(l, "verbs:"), "*") {
			t.Errorf("agent RBAC must stay read-only without Secrets: %q", l)
		}
	}
}

func TestOverrideEndpoints(t *testing.T) {
	srv, _, token := setup(t)
	u := srv.URL + "/api/v1"
	push(t, srv.URL, token, snap("v1.36.4+k3s1", map[string]string{"tool": "ghcr.io/acme/tool:0.4.0"}))
	if code, _ := call(t, "PUT", u+"/catalog/overrides", `{"repository":"ghcr.io/acme/tool","kind":"map","component":"nope"}`, true); code != 400 {
		t.Errorf("unknown component: %d", code)
	}
	if code, _ := call(t, "PUT", u+"/catalog/overrides", `{"repository":"ghcr.io/acme/tool:0.4.0","kind":"ignore"}`, true); code != 400 {
		t.Errorf("repository with tag: %d", code)
	}
	if code, _ := call(t, "PUT", u+"/catalog/overrides", `{"repository":"ghcr.io/acme/tool","kind":"inhouse"}`, true); code != 200 {
		t.Fatalf("set: %d", code)
	}
	_, inv := call(t, "GET", u+"/inventory", "", false)
	if len(inv["inHouse"].([]any)) != 1 || len(inv["unrecognized"].([]any)) != 0 {
		t.Errorf("override not applied: %v", inv)
	}
	if code, _ := call(t, "DELETE", u+"/catalog/overrides?repository=ghcr.io/acme/tool", "", true); code != 200 {
		t.Errorf("delete: %d", code)
	}
	_, inv = call(t, "GET", u+"/inventory", "", false)
	if len(inv["unrecognized"].([]any)) != 1 {
		t.Errorf("undo must bring it back: %v", inv)
	}
}
