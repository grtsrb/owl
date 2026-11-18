package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owl/internal/auth"
	"owl/internal/catalog"
	"owl/internal/server"
	"owl/internal/snapshot"
	"owl/internal/store"
	"owl/internal/store/sqlite"
)

// env is a real Owl server (in process) plus a CLI with its own config directory.
type env struct {
	t   *testing.T
	url string
	db  *sqlite.DB
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cat, _ := catalog.Builtin()
	srv := httptest.NewServer((&server.Server{Store: db, Catalog: cat, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler())
	t.Cleanup(srv.Close)
	ctx := context.Background()
	for _, u := range []struct {
		email string
		role  store.Role
	}{{"admin@example.com", store.RoleAdmin}, {"viewer@example.com", store.RoleViewer}} {
		user, _ := db.CreateUser(ctx, u.email, u.role)
		_ = db.SetPassword(ctx, user.ID, "$argon2id$unused", "")
	}
	// One cluster with a few images (no upstream data: levels are "unknown").
	c, _ := db.CreateCluster(ctx, "it-hetzner", store.StageStaging, "IT")
	s := &snapshot.Snapshot{SchemaVersion: snapshot.SchemaVersion, AgentVersion: "test", Cluster: snapshot.Cluster{KubernetesVersion: "v1.30.4+k3s1"}}
	for name, img := range map[string]string{"redis": "redis:7.2.4", "mystery": "example.com/acme/mystery:1.0"} {
		s.Workloads = append(s.Workloads, snapshot.Workload{Kind: "Deployment", Namespace: "seed", Name: name, DesiredReplicas: 1,
			Containers: []snapshot.Container{{Name: name, Image: img}}})
	}
	if _, err := db.Ingest(ctx, c.ID, s); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, url: srv.URL, db: db}
}

// token creates a personal API token directly in the store.
func (e *env) token(email string, scope store.TokenScope) string {
	ctx := context.Background()
	u, _ := e.db.UserByEmail(ctx, email)
	secret, hash, prefix := auth.NewAPIToken()
	name := "t" + prefix[len(prefix)-6:]
	if _, err := e.db.CreateAPIToken(ctx, store.APIToken{UserID: u.ID, Name: name, Prefix: prefix, Scope: scope}, hash); err != nil {
		e.t.Fatal(err)
	}
	return secret
}

// run runs one CLI command with stdin (not a terminal) and env vars; returns exit code, stdout, stderr.
func (e *env) run(stdin string, vars map[string]string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	a := &App{Version: "test", Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb,
		Getenv: func(k string) string { return vars[k] }, Terminal: func() bool { return false },
		ReadSecret: func() (string, error) { e.t.Fatal("ReadSecret without a terminal"); return "", nil }}
	code := a.Run(args[0], args[1:])
	return code, out.String(), errb.String()
}

func TestLoginWhoamiLogout(t *testing.T) {
	e := newEnv(t)
	tok := e.token("admin@example.com", store.ScopeRead)

	if code, _, errs := e.run("", nil, "status"); code != 1 || !strings.Contains(errs, "not signed in") {
		t.Fatalf("status before login: %d %s", code, errs)
	}
	if code, _, errs := e.run("not-a-token\n", nil, "login", "--server", e.url); code != 1 || !strings.Contains(errs, "owl_pat_") {
		t.Fatalf("login with garbage: %d %s", code, errs)
	}
	if code, _, errs := e.run(tok+"\n", nil, "login", tok); code != 2 || !strings.Contains(errs, "never passed on the command line") {
		t.Fatalf("token as argument: %d %s", code, errs)
	}
	code, out, errs := e.run(tok+"\n", nil, "login", "--server", e.url)
	if code != 0 || !strings.Contains(out, "admin@example.com (admin)") || !strings.Contains(out, "read-only") {
		t.Fatalf("login: %d %s %s", code, out, errs)
	}
	p, _ := configPath()
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode: %v %v", st, err)
	}
	if code, out, _ := e.run("", nil, "whoami"); code != 0 || !strings.Contains(out, e.url) {
		t.Fatalf("whoami: %d %s", code, out)
	}
	// Env overrides the file (CI).
	other := e.token("viewer@example.com", store.ScopeRead)
	if _, out, _ := e.run("", map[string]string{"OWL_TOKEN": other}, "whoami"); !strings.Contains(out, "viewer@example.com") {
		t.Fatalf("OWL_TOKEN override: %s", out)
	}

	// logout revokes the token on the server.
	if code, out, errs := e.run("", nil, "logout"); code != 0 || !strings.Contains(out, "revoked") {
		t.Fatalf("logout: %d %s %s", code, out, errs)
	}
	if code, _, errs := e.run("", map[string]string{"OWL_SERVER": e.url, "OWL_TOKEN": tok}, "whoami"); code != 1 || !strings.Contains(errs, "owl login") {
		t.Fatalf("revoked token still works: %d %s", code, errs)
	}
	if _, out, _ := e.run("", nil, "logout"); !strings.Contains(out, "Not signed in") {
		t.Fatalf("second logout: %s", out)
	}

	// --keep-token only forgets it.
	tok2 := e.token("admin@example.com", store.ScopeRead)
	e.run(tok2+"\n", nil, "login", "--server", e.url)
	if code, _, _ := e.run("", nil, "logout", "--keep-token"); code != 0 {
		t.Fatal("logout --keep-token")
	}
	if code, _, _ := e.run("", map[string]string{"OWL_SERVER": e.url, "OWL_TOKEN": tok2}, "whoami"); code != 0 {
		t.Fatal("--keep-token revoked the token")
	}
	if c, _ := loadConfig(); c.Token != "" || c.Server == "" {
		t.Fatalf("config after logout: %+v", c)
	}
}

func TestPlainHTTPRefused(t *testing.T) {
	if _, err := checkServerURL("http://owl.example.com"); err == nil {
		t.Fatal("plain http to a remote host accepted")
	}
	for _, ok := range []string{"https://owl.example.com", "http://127.0.0.1:8080", "http://localhost:8080/", "http://[::1]:8080"} {
		if _, err := checkServerURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"owl.example.com", "ftp://x", "https://user:pw@x", "https://x/?a=b"} {
		if _, err := checkServerURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestReadCommandsAndFailOn(t *testing.T) {
	e := newEnv(t)
	vars := map[string]string{"OWL_SERVER": e.url, "OWL_TOKEN": e.token("viewer@example.com", store.ScopeRead)}

	if code, out, errs := e.run("", vars, "status"); code != 0 || !strings.Contains(out, "it-hetzner") || !strings.Contains(out, "Unrecognized images: 1") {
		t.Fatalf("status: %d %s %s", code, out, errs)
	}
	if code, out, _ := e.run("", vars, "clusters", "-o", "json"); code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Fatalf("clusters json: %d %s", code, out)
	}
	if code, out, _ := e.run("", vars, "unrecognized"); code != 0 || !strings.Contains(out, "example.com/acme/mystery") {
		t.Fatalf("unrecognized: %d %s", code, out)
	}
	if code, out, _ := e.run("", vars, "changes"); code != 0 || !strings.Contains(out, "it-hetzner") {
		t.Fatalf("changes: %d %s", code, out)
	}
	if code, out, errs := e.run("", vars, "component", "redis"); code != 0 || !strings.Contains(out, "Redis") || !strings.Contains(out, "seed/redis") {
		t.Fatalf("component: %d %s %s", code, out, errs)
	}

	// Without upstream data everything is "unknown": below the default --level low.
	if code, out, _ := e.run("", vars, "outdated"); code != 0 || !strings.Contains(out, "Nothing at low or above") {
		t.Fatalf("outdated: %d %s", code, out)
	}
	if code, out, _ := e.run("", vars, "outdated", "--level", "unknown", "--fail-on", "critical"); code != 0 || !strings.Contains(out, "redis") {
		t.Fatalf("outdated --level unknown: %d %s", code, out)
	}
	if code, _, errs := e.run("", vars, "outdated", "--all", "--fail-on", "unknown"); code != 1 || !strings.Contains(errs, "--fail-on unknown") {
		t.Fatalf("fail-on: %d %s", code, errs)
	}
	if code, out, _ := e.run("", vars, "outdated", "--all", "--cluster", "nope", "-o", "json", "--fail-on", "unknown"); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("filtered out: %d %s", code, out)
	}
	if code, _, _ := e.run("", vars, "outdated", "--level", "bad"); code != 2 {
		t.Fatalf("bad level: %d", code)
	}
}

func TestClusterWrites(t *testing.T) {
	e := newEnv(t)
	read := map[string]string{"OWL_SERVER": e.url, "OWL_TOKEN": e.token("admin@example.com", store.ScopeRead)}
	write := map[string]string{"OWL_SERVER": e.url, "OWL_TOKEN": e.token("admin@example.com", store.ScopeWrite)}

	if code, _, errs := e.run("", read, "cluster", "add", "it-new", "--stage", "staging", "--tenant", "IT"); code != 1 || !strings.Contains(errs, "read-only") {
		t.Fatalf("read token added a cluster: %d %s", code, errs)
	}
	code, out, errs := e.run("", write, "cluster", "add", "it-new", "--stage", "staging", "--tenant", "IT")
	if code != 0 || !strings.HasPrefix(out, "inv_") || !strings.Contains(errs, "shown only once") {
		t.Fatalf("cluster add: %d %q %s", code, out, errs)
	}
	if _, err := e.db.ClusterByTokenHash(context.Background(), auth.Hash(strings.TrimSpace(out))); err != nil {
		t.Fatalf("printed token doesn't work: %v", err)
	}
	code, out, _ = e.run("", write, "cluster", "rotate-token", "it-new", "-o", "secret")
	if code != 0 || !strings.Contains(out, "kind: Secret") || !strings.Contains(out, `OWL_TOKEN: "inv_`) {
		t.Fatalf("rotate -o secret: %d %s", code, out)
	}
	// Old database-file syntax: pointed to `owl db`.
	if code, _, errs := e.run("", write, "cluster", "add", "--name", "x", "--stage", "live", "--tenant", "IT"); code != 1 || !strings.Contains(errs, "owl db cluster") {
		t.Fatalf("legacy syntax: %d %s", code, errs)
	}
	// Delete needs confirmation; without a terminal, --yes.
	if code, _, errs := e.run("", write, "cluster", "delete", "it-new"); code != 1 || !strings.Contains(errs, "--yes") {
		t.Fatalf("delete without --yes: %d %s", code, errs)
	}
	if code, _, errs := e.run("", write, "cluster", "delete", "it-new", "--yes"); code != 0 {
		t.Fatalf("delete: %d %s", code, errs)
	}
	audit, _ := e.db.ListAudit(context.Background(), 1)
	if len(audit) != 1 || audit[0].Action != "cluster deleted" || !strings.HasPrefix(audit[0].Actor, "admin@example.com (token: ") {
		t.Fatalf("audit: %+v", audit)
	}
}
