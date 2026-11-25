package cli

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owl/internal/catalog"
	"owl/internal/scan"
	"owl/internal/server"
	"owl/internal/snapshot"
	"owl/internal/store"
	"owl/internal/store/sqlite"
)

type fakeTrivy struct{}

func (fakeTrivy) Scan(_ context.Context, t scan.Target, _ *scan.Credential) (*scan.Result, error) {
	if strings.Contains(t.Ref, "team/app") {
		return &scan.Result{Vulns: []scan.Vuln{
			{ID: "CVE-A", Severity: "critical", Fixed: "1.1", Package: "openssl", Installed: "1.0", Class: "os"},
			{ID: "CVE-B", Severity: "high", Package: "zlib", Installed: "1.2", Class: "os"},
		}}, nil
	}
	return &scan.Result{Vulns: []scan.Vuln{{ID: "CVE-H", Severity: "high", Fixed: "2", Package: "libc", Installed: "1", Class: "os"}}}, nil
}
func (fakeTrivy) UpdateDB(context.Context) error { return nil }
func (fakeTrivy) DBInfo(context.Context) (scan.DBInfo, error) {
	return scan.DBInfo{UpdatedAt: time.Now(), Downloaded: time.Now()}, nil
}

func TestVulnsCommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	u, _ := db.CreateUser(ctx, "viewer@example.com", store.RoleViewer)
	_ = db.SetPassword(ctx, u.ID, "$argon2id$unused", "")
	_ = db.SetSetting(ctx, "scanLastDaily", time.Now().UTC().Format(time.RFC3339))
	c, _ := db.CreateCluster(ctx, "it-hetzner", store.StageStaging, "IT")
	app := "sha256:" + strings.Repeat("a", 64)
	redis := "sha256:" + strings.Repeat("b", 64)
	sn := &snapshot.Snapshot{SchemaVersion: snapshot.SchemaVersion, AgentVersion: "test", Cluster: snapshot.Cluster{KubernetesVersion: "v1.30.4+k3s1"}}
	for _, w := range []struct{ name, image, id string }{
		{"app", "registry.gitlab.example.com/team/app:1.0", "registry.gitlab.example.com/team/app@" + app},
		{"redis", "redis:7.2.4", "docker.io/library/redis@" + redis},
		{"local", "local/tool:dev", "sha256:" + strings.Repeat("c", 64)}, // built on the node: not scannable
	} {
		sn.Workloads = append(sn.Workloads, snapshot.Workload{Kind: "Deployment", Namespace: "seed", Name: w.name, DesiredReplicas: 1,
			Containers: []snapshot.Container{{Name: w.name, Image: w.image}},
			Running:    []snapshot.RunningImage{{Container: w.name, Image: w.image, ImageID: w.id, Pods: 1, Ready: 1}}})
	}
	if _, err := db.Ingest(ctx, c.ID, sn); err != nil {
		t.Fatal(err)
	}
	cat, _ := catalog.Builtin()
	cat.InternalRegistries = []string{"registry.gitlab.example.com"}
	s := &server.Server{Store: db, Catalog: cat, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_ = s.ReloadCatalog(ctx)
	s.Scans = &scan.Manager{Scanner: fakeTrivy{}, Store: db, Targets: s.ScanTargets}
	_ = s.Scans.Load(ctx)
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go s.Scans.Run(runCtx)
	for range 400 {
		if len(s.Scans.Results()) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	e := &env{t: t, url: srv.URL, db: db}
	vars := map[string]string{"OWL_SERVER": srv.URL, "OWL_TOKEN": e.token("viewer@example.com", store.ScopeRead)}

	code, out, errOut := e.run("", vars, "vulns")
	if code != 0 || !strings.Contains(out, "CRITICAL") || strings.Index(out, "team/app") > strings.Index(out, "redis") {
		t.Fatalf("vulns: %d\n%s\n%s", code, out, errOut)
	}
	if code, out, _ := e.run("", vars, "vulns", "--in-house"); code != 0 || strings.Contains(out, "redis") {
		t.Errorf("--in-house: %d\n%s", code, out)
	}
	if code, _, errOut := e.run("", vars, "vulns", "--fail-on", "critical"); code != 1 || !strings.Contains(errOut, "fail-on critical") {
		t.Errorf("--fail-on critical: %d %s", code, errOut)
	}
	// Redis has only a fixable High: critical passes, high fails
	if code, _, _ := e.run("", vars, "vulns", "--image", "docker.io/library/redis", "--fail-on", "critical"); code != 0 {
		t.Errorf("redis --fail-on critical: %d", code)
	}
	if code, _, _ := e.run("", vars, "vulns", "--image", "docker.io/library/redis", "--fail-on", "high"); code != 1 {
		t.Errorf("redis --fail-on high: %d", code)
	}
	code, out, _ = e.run("", vars, "vulns", "--image", app)
	if code != 0 || !strings.Contains(out, "CVE-A") || !strings.Contains(out, "openssl") || !strings.Contains(out, "CVE-B") {
		t.Errorf("--image: %d\n%s", code, out)
	}
	if code, _, _ := e.run("", vars, "vulns", "--image", "nothing/here"); code != 1 {
		t.Errorf("no match: %d", code)
	}
	if code, out, _ := e.run("", vars, "vulns", "-o", "json"); code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("json: %d %s", code, out)
	}
	// no scan result fails the gate, unless allowed
	if code, _, errOut := e.run("", vars, "vulns", "--image", "docker.io/local/tool", "--fail-on", "critical"); code != 1 || !strings.Contains(errOut, "no scan result (not scannable)") {
		t.Errorf("unscanned image passed the gate: %d %s", code, errOut)
	}
	if code, _, _ := e.run("", vars, "vulns", "--image", "docker.io/local/tool", "--fail-on", "critical", "--allow-unscanned"); code != 0 {
		t.Errorf("--allow-unscanned: %d", code)
	}
	if code, _, _ := e.run("", vars, "vulns", "--fail-on", "medium"); code != 1 {
		t.Errorf("bad --fail-on accepted: %d", code)
	}
}
