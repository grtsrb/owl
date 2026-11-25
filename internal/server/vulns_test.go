package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"owl/internal/auth"
	"owl/internal/catalog"
	"owl/internal/scan"
	"owl/internal/snapshot"
	"owl/internal/store"
	"owl/internal/store/sqlite"
	"owl/internal/upstream"
)

type fakeTrivy struct {
	mu        sync.Mutex
	results   map[string]*scan.Result // by repository
	platforms []string
	password  map[string]string // repository → the password a pull needs (ErrAuth otherwise)
}

func (f *fakeTrivy) Scan(_ context.Context, t scan.Target, cred *scan.Credential) (*scan.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.platforms = append(f.platforms, t.Platform)
	if pw, ok := f.password[t.Repository]; ok && (cred == nil || cred.Password != pw) {
		return nil, scan.ErrAuth
	}
	if r := f.results[t.Repository]; r != nil {
		return r, nil
	}
	return &scan.Result{Vulns: []scan.Vuln{}}, nil
}
func (f *fakeTrivy) UpdateDB(context.Context) error { return nil }
func (f *fakeTrivy) DBInfo(context.Context) (scan.DBInfo, error) {
	return scan.DBInfo{Version: "0.76.2", UpdatedAt: time.Now(), Downloaded: time.Now()}, nil
}

func digestOf(c string) string { return "sha256:" + strings.Repeat(c, 64) }

// setupVulns: a server with scanning on (fake Trivy), one cluster (arm64 nodes) running an in-house
// image, Redis 7.2.4 (Owl lists CVE-OWL for it; Trivy finds it too) and an image built on the node.
func setupVulns(t *testing.T) (*httptest.Server, *sqlite.DB, *Server, *fakeTrivy) {
	srv, db, s, ft, _ := setupVulnsToken(t)
	return srv, db, s, ft
}

func setupVulnsToken(t *testing.T) (*httptest.Server, *sqlite.DB, *Server, *fakeTrivy, string) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	addTestUsers(t, db)
	ctx := context.Background()
	c, _ := db.CreateCluster(ctx, "it-hetzner", store.StageStaging, "IT")
	token, hash, prefix := auth.NewToken()
	_ = db.AddToken(ctx, c.ID, hash, prefix)
	cat, _ := catalog.Builtin()
	cat.InternalRegistries = []string{"registry.gitlab.example.com"}
	ft := &fakeTrivy{results: map[string]*scan.Result{
		"registry.gitlab.example.com/team/app": {OS: "alpine 3.20", Vulns: []scan.Vuln{
			{ID: "CVE-A", Severity: "critical", Fixed: "1.1", Package: "openssl", Class: "os"},
			{ID: "CVE-B", Severity: "critical", Fixed: "2.0", Package: "zlib", Class: "os"},
			{ID: "CVE-C", Severity: "high", Package: "busybox", Class: "os"}, // no fix
		}},
		"docker.io/library/redis": {OS: "debian 12.5", Vulns: []scan.Vuln{
			{ID: "CVE-OWL", Severity: "high", Fixed: "7.2.5", Package: "redis", Class: "lang"},
			{ID: "CVE-D", Severity: "high", Fixed: "3.0", Package: "libc", Class: "os"},
		}},
	}}
	s := &Server{Store: db, Catalog: cat, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Upstream: &upstream.Client{}}
	s.upstream = map[string]*upstream.Info{"redis": {ComponentID: "redis", Advisories: []upstream.Advisory{{
		ID: "GHSA-owl", Aliases: []string{"CVE-OWL"}, Severity: "high",
		Ranges: []upstream.AffectedRange{{Range: ">= 7.0.0, < 7.2.5", Patched: "7.2.5"}},
	}}}}
	s.Scans = &scan.Manager{Scanner: ft, Store: db, Targets: s.ScanTargets}
	_ = db.SetSetting(ctx, "scanLastDaily", time.Now().UTC().Format(time.RFC3339))
	if err := s.ReloadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Scans.Load(ctx); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go s.Scans.Run(runCtx)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	sn := &snapshot.Snapshot{SchemaVersion: snapshot.SchemaVersion, AgentVersion: "test", Cluster: snapshot.Cluster{
		KubernetesVersion: "v1.33.6+k3s1", Nodes: []snapshot.Node{{Name: "n1", Architecture: "arm64"}}}}
	for _, w := range []struct{ name, image, id string }{
		{"app", "registry.gitlab.example.com/team/app:1.0", "registry.gitlab.example.com/team/app@" + digestOf("a")},
		{"redis", "redis:7.2.4", "docker.io/library/redis@" + digestOf("b")},
		{"local", "local/tool:dev", digestOf("c")},                                // built on the node
		{"meta", "169.254.169.254/x/y:1", "169.254.169.254/x/y@" + digestOf("9")}, // a lying agent
	} {
		sn.Workloads = append(sn.Workloads, snapshot.Workload{Kind: "Deployment", Namespace: "seed", Name: w.name, DesiredReplicas: 1,
			Containers: []snapshot.Container{{Name: w.name, Image: w.image}},
			Running:    []snapshot.RunningImage{{Container: w.name, Image: w.image, ImageID: w.id, Pods: 1, Ready: 1}}})
	}
	if code := push(t, srv.URL, token, sn); code != http.StatusAccepted {
		t.Fatalf("push: %d", code)
	}
	waitScans(t, s, 2)
	return srv, db, s, ft, token
}

func waitScans(t *testing.T, s *Server, n int) {
	t.Helper()
	for range 400 {
		if len(s.Scans.Results()) >= n && len(s.Scans.State()) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("scans: %d results", len(s.Scans.Results()))
}

func images(t *testing.T, base string) map[string]map[string]any {
	t.Helper()
	code, out := call(t, "GET", base+"/api/v1/vulns", "", false)
	if code != 200 || out["enabled"] != true {
		t.Fatalf("vulns: %d %v", code, out)
	}
	by := map[string]map[string]any{}
	for _, x := range out["images"].([]any) {
		img := x.(map[string]any)
		by[img["key"].(string)] = img
	}
	return by
}

func TestVulnsList(t *testing.T) {
	srv, _, _, ft := setupVulns(t)
	by := images(t, srv.URL)
	app, redis, local := by[digestOf("a")], by[digestOf("b")], by[digestOf("c")]
	if app == nil || redis == nil || local == nil {
		t.Fatalf("images = %v", by)
	}
	fix := func(img map[string]any) map[string]any { return img["fixable"].(map[string]any) }
	if app["inHouse"] != true || app["state"] != "ok" || fix(app)["critical"] != 2.0 || fix(app)["high"] != 0.0 ||
		app["total"].(map[string]any)["high"] != 1.0 || app["os"] != "alpine 3.20" {
		t.Errorf("app = %v", app)
	}
	// CVE-OWL is Owl's own advisory for Redis 7.2.4: shown there, not counted again
	if redis["inHouse"] != false || fix(redis)["high"] != 1.0 || redis["confirmed"] != 1.0 {
		t.Errorf("redis = %v", redis)
	}
	if comps := redis["components"].([]any); len(comps) != 1 || comps[0] != "redis" {
		t.Errorf("redis components = %v", comps)
	}
	if local["state"] != "not scannable" || local["digest"] != nil {
		t.Errorf("local = %v", local)
	}
	// an image ID naming an IP-literal registry is never pulled (SSRF from a leaked agent token)
	meta := by["169.254.169.254/x/y@"+digestOf("9")]
	if meta == nil || meta["state"] != "not scannable" || !strings.Contains(meta["error"].(string), "IP address") {
		t.Errorf("meta = %v", meta)
	}
	ft.mu.Lock()
	if n := len(ft.platforms); n != 2 {
		t.Errorf("scanned %d images, want 2 (never the metadata address)", n)
	}
	ft.mu.Unlock()
	// in-house first
	_, out := call(t, "GET", srv.URL+"/api/v1/vulns", "", false)
	if first := out["images"].([]any)[0].(map[string]any); first["key"] != digestOf("a") {
		t.Errorf("first = %v", first["key"])
	}
	ft.mu.Lock()
	for _, p := range ft.platforms {
		if p != "linux/arm64" {
			t.Errorf("scanned for %q, want the nodes' platform", p)
		}
	}
	ft.mu.Unlock()

	// detail: CVE-OWL is "confirmed", not in the Trivy list
	code, d := call(t, "GET", srv.URL+"/api/v1/vulns/images/"+digestOf("b"), "", false)
	if code != 200 {
		t.Fatalf("detail: %d %v", code, d)
	}
	if vs := d["vulns"].([]any); len(vs) != 1 || vs[0].(map[string]any)["id"] != "CVE-D" {
		t.Errorf("vulns = %v", vs)
	}
	if cf := d["confirmed"].([]any); len(cf) != 1 || cf[0].(map[string]any)["id"] != "CVE-OWL" {
		t.Errorf("confirmed = %v", cf)
	}
	if code, _ := call(t, "GET", srv.URL+"/api/v1/vulns/images/sha256:nope", "", false); code != 400 {
		t.Errorf("bad digest: %d", code)
	}
	if code, _ := call(t, "GET", srv.URL+"/api/v1/vulns/images/"+digestOf("f"), "", false); code != 404 {
		t.Errorf("unknown digest: %d", code)
	}

	// component page: its images and the confirmed ids
	_, comp := call(t, "GET", srv.URL+"/api/v1/components/redis", "", false)
	if sc := comp["imageScans"].([]any); len(sc) != 1 {
		t.Errorf("imageScans = %v", sc)
	}
	if cf := comp["trivyConfirmed"].([]any); len(cf) != 1 || cf[0] != "CVE-OWL" {
		t.Errorf("trivyConfirmed = %v", cf)
	}
}

func TestVulnAcceptances(t *testing.T) {
	srv, db, _, _ := setupVulns(t)
	repo := "registry.gitlab.example.com/team/app"
	body := `{"repository":"` + repo + `","vulnId":"CVE-A","reason":"not reachable: no TLS in this service","expiresAt":"2099-01-31"}`
	if code, _ := call(t, "PUT", srv.URL+"/api/v1/vulns/acceptances", body, false); code != http.StatusForbidden {
		t.Errorf("viewer accepted: %d", code)
	}
	if code, out := call(t, "PUT", srv.URL+"/api/v1/vulns/acceptances", body, true); code != http.StatusNoContent {
		t.Fatalf("accept: %d %v", code, out)
	}
	app := images(t, srv.URL)[digestOf("a")]
	if app["fixable"].(map[string]any)["critical"] != 1.0 || app["accepted"] != 1.0 {
		t.Errorf("after accept: %v", app)
	}
	_, d := call(t, "GET", srv.URL+"/api/v1/vulns/images/"+digestOf("a"), "", false)
	vs := d["vulns"].([]any)
	if last := vs[len(vs)-1].(map[string]any); last["id"] != "CVE-A" || last["accepted"] == nil {
		t.Errorf("accepted ones go last, marked: %v", vs)
	}
	as, _ := db.ListVulnAcceptances(context.Background())
	if len(as) != 1 || as[0].CreatedBy != "admin@example.com" || as[0].ExpiresAt == nil {
		t.Errorf("stored = %+v", as)
	}
	audit, _ := db.ListAudit(context.Background(), 10)
	if len(audit) == 0 || audit[0].Action != "vulnerability accepted" {
		t.Errorf("audit = %+v", audit)
	}

	// an expired acceptance counts again (and says so)
	past := time.Now().Add(-time.Hour)
	_ = db.PutVulnAcceptance(context.Background(), store.VulnAcceptance{Repository: repo, VulnID: "CVE-B", Reason: "old", ExpiresAt: &past, CreatedBy: "x"})
	if app := images(t, srv.URL)[digestOf("a")]; app["fixable"].(map[string]any)["critical"] != 1.0 {
		t.Errorf("expired acceptance must count: %v", app)
	}

	for _, bad := range []string{
		`{"repository":"","vulnId":"CVE-A","reason":"x"}`,
		`{"repository":"a b","vulnId":"CVE-A","reason":"x"}`,
		`{"repository":"r","vulnId":"<script>","reason":"x"}`,
		`{"repository":"r","vulnId":"CVE-A","reason":"  "}`,
		`{"repository":"r","vulnId":"CVE-A","reason":"x","expiresAt":"2020-01-01"}`,
		`{"repository":"r","vulnId":"CVE-A","reason":"x","expiresAt":"tomorrow"}`,
	} {
		if code, _ := call(t, "PUT", srv.URL+"/api/v1/vulns/acceptances", bad, true); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	del := `{"repository":"` + repo + `","vulnId":"CVE-A"}`
	if code, _ := call(t, "DELETE", srv.URL+"/api/v1/vulns/acceptances", del, true); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if code, _ := call(t, "DELETE", srv.URL+"/api/v1/vulns/acceptances", del, true); code != 404 {
		t.Errorf("delete again: %d", code)
	}
}

func TestScannerEndpoints(t *testing.T) {
	srv, _, s, _ := setupVulns(t)
	code, st := call(t, "GET", srv.URL+"/api/v1/vulns/scanner", "", false)
	if code != 200 || st["enabled"] != true {
		t.Fatalf("scanner: %d %v", code, st)
	}
	status := st["status"].(map[string]any)
	if recent := status["recent"].([]any); len(recent) != 2 || status["db"].(map[string]any)["version"] != "0.76.2" {
		t.Errorf("status = %v", status)
	}
	path := srv.URL + "/api/v1/vulns/images/" + digestOf("a") + "/rescan"
	if code, _ := call(t, "POST", path, "", false); code != http.StatusForbidden {
		t.Errorf("viewer rescan: %d", code)
	}
	if code, out := call(t, "POST", path, "", true); code != 200 || out["queued"] != true {
		t.Errorf("rescan: %d %v", code, out)
	}
	waitScans(t, s, 2)
	if code, _ := call(t, "POST", srv.URL+"/api/v1/vulns/images/"+digestOf("f")+"/rescan", "", true); code != 404 {
		t.Errorf("rescan of an image that doesn't run: %d", code)
	}
	if code, _ := call(t, "POST", srv.URL+"/api/v1/vulns/scanner/cancel", `{"digest":"`+digestOf("a")+`"}`, true); code != 404 {
		t.Errorf("cancel of nothing: %d", code)
	}
	if code, _ := call(t, "POST", srv.URL+"/api/v1/vulns/scanner/cancel", `{"digest":"`+digestOf("a")+`"}`, false); code != http.StatusForbidden {
		t.Errorf("viewer cancel: %d", code)
	}
}

// API tokens can read vulnerabilities but never change acceptances or the queue (not in tokenWrites).
func TestVulnWritesClosedToTokens(t *testing.T) {
	for _, p := range []string{"POST /api/v1/vulns/images/{digest}/rescan", "POST /api/v1/vulns/scanner/cancel",
		"PUT /api/v1/vulns/acceptances", "DELETE /api/v1/vulns/acceptances"} {
		if tokenWrites[p] {
			t.Errorf("%s must stay browser-only", p)
		}
	}
}

func TestScanningOff(t *testing.T) {
	srv, _, _ := setup(t)
	code, out := call(t, "GET", srv.URL+"/api/v1/vulns", "", false)
	if code != 200 || out["enabled"] != false {
		t.Errorf("vulns: %d %v", code, out)
	}
	if code, out := call(t, "GET", srv.URL+"/api/v1/vulns/scanner", "", false); code != 200 || out["enabled"] != false {
		t.Errorf("scanner: %d %v", code, out)
	}
	if code, _ := call(t, "POST", srv.URL+"/api/v1/vulns/images/"+digestOf("a")+"/rescan", "", true); code != http.StatusConflict {
		t.Errorf("rescan with scanning off: %d", code)
	}
}

// A new digest with an unchanged template (pods restarted, or a re-pushed tag) is scanned at once, and a
// digest left from a rollout is matched with its own version's advisories.
func TestVulnRolloutDigests(t *testing.T) {
	srv, _, s, _, token := setupVulnsToken(t)
	sn := &snapshot.Snapshot{SchemaVersion: snapshot.SchemaVersion, AgentVersion: "test", Cluster: snapshot.Cluster{
		KubernetesVersion: "v1.33.6+k3s1", Nodes: []snapshot.Node{{Name: "n1", Architecture: "arm64"}}}}
	workloads := func(running ...snapshot.RunningImage) {
		sn.Workloads = []snapshot.Workload{
			{Kind: "Deployment", Namespace: "seed", Name: "app", DesiredReplicas: 1,
				Containers: []snapshot.Container{{Name: "app", Image: "registry.gitlab.example.com/team/app:1.0"}},
				Running: []snapshot.RunningImage{{Container: "app", Image: "registry.gitlab.example.com/team/app:1.0",
					ImageID: "registry.gitlab.example.com/team/app@" + digestOf("a"), Pods: 1, Ready: 1}}},
			{Kind: "Deployment", Namespace: "seed", Name: "redis", DesiredReplicas: 2,
				Containers: []snapshot.Container{{Name: "redis", Image: "redis:7.2.5"}}, Running: running},
		}
	}
	// the template moved to 7.2.5; one pod still runs 7.2.4 (digest b), one runs 7.2.5 (digest d)
	workloads(snapshot.RunningImage{Container: "redis", Image: "redis:7.2.4", ImageID: "docker.io/library/redis@" + digestOf("b"), Pods: 1, Ready: 1},
		snapshot.RunningImage{Container: "redis", Image: "redis:7.2.5", ImageID: "docker.io/library/redis@" + digestOf("d"), Pods: 1, Ready: 1})
	if code := push(t, srv.URL, token, sn); code != http.StatusAccepted {
		t.Fatalf("push: %d", code)
	}
	waitScans(t, s, 3)
	by := images(t, srv.URL)
	if old := by[digestOf("b")]; old["confirmed"] != 1.0 || old["fixable"].(map[string]any)["high"] != 1.0 {
		t.Errorf("7.2.4 digest: CVE-OWL affects 7.2.4, so it's confirmed, not counted: %v", old)
	}
	if cur := by[digestOf("d")]; cur["confirmed"] != 0.0 || cur["fixable"].(map[string]any)["high"] != 2.0 {
		t.Errorf("7.2.5 digest: CVE-OWL is fixed in 7.2.5, so Trivy's finding counts: %v", cur)
	}
	// same template, pods replaced with a new digest (e): no template change, still scanned right away
	workloads(snapshot.RunningImage{Container: "redis", Image: "redis:7.2.5", ImageID: "docker.io/library/redis@" + digestOf("e"), Pods: 2, Ready: 2})
	if code := push(t, srv.URL, token, sn); code != http.StatusAccepted {
		t.Fatalf("push: %d", code)
	}
	for range 400 {
		if r := s.Scans.Results()[digestOf("e")]; r != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the new digest wasn't scanned after a push without template changes")
}
