package scan

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeRegistry(t *testing.T, challenge func(host string) string, tokenOK func(r *http.Request) bool) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	var host string
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok && u == "u" && p == "pw" && strings.HasPrefix(challenge(host), "Basic") {
			return
		}
		w.Header().Set("WWW-Authenticate", challenge(host))
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/jwt/auth", func(w http.ResponseWriter, r *http.Request) {
		if !tokenOK(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"token":"t"}`))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	host = strings.TrimPrefix(srv.URL, "https://")
	return srv, host
}

func TestTestLogin(t *testing.T) {
	neverRegistry = func(string) string { return "" } // the fake registries run on 127.0.0.1
	t.Cleanup(func() { neverRegistry = NeverRegistry })
	var scope string
	bearer := func(host string) string {
		return `Bearer realm="https://` + host + `/jwt/auth",service="container_registry"`
	}
	ok := func(r *http.Request) bool {
		u, p, _ := r.BasicAuth()
		scope = r.URL.Query().Get("scope")
		return u == "u" && p == "pw" && r.URL.Query().Get("service") == "container_registry"
	}
	srv, host := fakeRegistry(t, bearer, ok)
	ctx := context.Background()
	if r := TestLogin(ctx, srv.Client(), Credential{Prefix: host + "/team-a", Username: "u", Password: "pw"}, ""); r.Outcome != LoginOK || scope != "repository:team-a:pull" {
		t.Fatalf("bearer ok: %+v scope %q", r, scope)
	}
	r := TestLogin(ctx, srv.Client(), Credential{Prefix: host, Username: "u", Password: "wrong-secret"}, "")
	if r.Outcome != LoginRejected || strings.Contains(r.Message, "wrong-secret") {
		t.Fatalf("bearer rejected: %+v", r)
	}

	// Basic challenge.
	srvB, hostB := fakeRegistry(t, func(string) string { return `Basic realm="Registry"` }, ok)
	if r := TestLogin(ctx, srvB.Client(), Credential{Prefix: hostB, Username: "u", Password: "pw"}, ""); r.Outcome != LoginOK {
		t.Fatalf("basic: %+v", r)
	}
	if r := TestLogin(ctx, srvB.Client(), Credential{Prefix: hostB, Username: "u", Password: "x"}, ""); r.Outcome != LoginRejected {
		t.Fatalf("basic rejected: %+v", r)
	}

	// A realm on the metadata service or over plain HTTP: refused, the password never sent there.
	for _, realm := range []string{"https://169.254.169.254/latest", "http://auth.example.com/jwt", "https://localhost/jwt"} {
		srvM, hostM := fakeRegistry(t, func(string) string { return `Bearer realm="` + realm + `"` }, ok)
		if r := TestLogin(ctx, srvM.Client(), Credential{Prefix: hostM, Username: "u", Password: "pw"}, ""); r.Outcome != LoginUnexpected {
			t.Fatalf("realm %s: %+v", realm, r)
		}
	}

	// Not resolvable / not a TLS server.
	if r := TestLogin(ctx, &http.Client{Timeout: 5 * time.Second}, Credential{Prefix: "owl-no-such-host.invalid", Username: "u", Password: "pw"}, ""); r.Outcome != LoginDNS {
		t.Fatalf("dns: %+v", r)
	}
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	if r := TestLogin(ctx, nil, Credential{Prefix: strings.TrimPrefix(plain.URL, "http://"), Username: "u", Password: "pw"}, ""); r.Outcome != LoginTLS {
		t.Fatalf("tls: %+v", r)
	}
}

func TestCredentialExpiry(t *testing.T) {
	cs, err := ParseCredentials([]byte("- {prefix: a.example.com, username: u, password: p, expires: 2027-01-10}\n- {prefix: b.example.com, username: u, password: p}\n"))
	if err != nil || cs[0].ExpiresAt == nil || cs[0].ExpiresAt.Format(time.RFC3339) != "2027-01-10T23:59:59Z" || !cs[0].FromFile || cs[1].ExpiresAt != nil {
		t.Fatalf("parse: %+v %v", cs, err)
	}
	if _, err := ParseCredentials([]byte("- {prefix: a.example.com, username: u, password: p, expires: soon}")); err == nil {
		t.Fatal("bad date accepted")
	}
	day := func(s string) time.Time { d, _ := time.Parse("2006-01-02", s); return d }
	if e := cs.Expiring(day("2026-12-01")); len(e) != 0 {
		t.Fatalf("too early: %+v", e)
	}
	if e := cs.Expiring(day("2026-12-30")); len(e) != 1 || e[0].Expired {
		t.Fatalf("soon: %+v", e)
	}
	if e := cs.Expiring(day("2027-01-11")); len(e) != 1 || !e[0].Expired {
		t.Fatalf("expired: %+v", e)
	}
}

func TestTestLoginRefusesLocalHosts(t *testing.T) {
	for _, p := range []string{"169.254.169.254", "localhost:5000/team", "[fd00:ec2::254]"} {
		if r := TestLogin(context.Background(), &http.Client{Timeout: time.Second}, Credential{Prefix: p, Username: "u", Password: "pw"}, ""); r.Outcome != LoginUnexpected || !strings.Contains(r.Message, "never a registry") {
			t.Errorf("%s: %+v", p, r)
		}
	}
}

// GitLab logs in on its web host (/jwt/auth), not the registry host: behind a VPN / ziti that host can be
// unreachable while the registry answers. The message must name the login host, not blame the password.
func TestTestLoginRealmUnreachable(t *testing.T) {
	neverRegistry = func(string) string { return "" }
	t.Cleanup(func() { neverRegistry = NeverRegistry })
	srv, host := fakeRegistry(t, func(string) string {
		return `Bearer realm="https://gitlab.owl-no-such-host.invalid/jwt/auth",service="container_registry"`
	}, func(*http.Request) bool { return true })
	r := TestLogin(context.Background(), srv.Client(), Credential{Prefix: host + "/devops", Username: "u", Password: "pw"}, "")
	if r.Outcome != LoginDNS || !strings.Contains(r.Message, "gitlab.owl-no-such-host.invalid") ||
		!strings.Contains(r.Message, host+" answered") || strings.Contains(r.Message, "pw") {
		t.Fatalf("realm unreachable: %+v", r)
	}
}

func TestRepositoryOf(t *testing.T) {
	for in, want := range map[string]string{
		"registry.x/devops/billing/api:1.0": "registry.x/devops/billing/api",
		"Registry.X/devops/api@sha256:abc":  "registry.x/devops/api",
		"registry.x:5000/team/app:2":        "registry.x:5000/team/app",
		"registry.x:5000/team/app":          "registry.x:5000/team/app",
		"nginx:1.27":                        "docker.io/library/nginx",
		"bitnami/redis:7":                   "docker.io/bitnami/redis",
		"localhost:5000/a/b:1":              "localhost:5000/a/b",
		"":                                  "",
		"a b/c":                             "",
		"registry.x/devops/api:1.0@sha256:0123456789": "registry.x/devops/api",
	} {
		if got := RepositoryOf(in); got != want {
			t.Errorf("RepositoryOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// A login used for several prefixes is renewed once: one expiry warning per login, not per prefix.
func TestExpiringOncePerLogin(t *testing.T) {
	now := time.Now()
	soon := now.Add(48 * time.Hour)
	cs := Credentials{
		{Prefix: "r/a", Login: "team", ExpiresAt: &soon},
		{Prefix: "r/b", Login: "team", ExpiresAt: &soon},
		{Prefix: "r/file", ExpiresAt: &soon, FromFile: true},
	}
	got := cs.Expiring(now)
	if len(got) != 2 || got[0].Login != "team" || got[1].Login != "r/file" {
		t.Fatalf("expiring = %+v", got)
	}
}

// A token endpoint hands out a token for any valid login, also for a repository the login can't read
// (GitLab: an empty "access" list). With a repository, Test checks the token's access claim.
func TestTestLoginChecksReadAccess(t *testing.T) {
	neverRegistry = func(string) string { return "" }
	t.Cleanup(func() { neverRegistry = NeverRegistry })
	jwt := func(access string) string {
		enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
		return enc(`{"alg":"none"}`) + "." + enc(`{"access":`+access+`}`) + ".sig"
	}
	mux := http.NewServeMux()
	var host string
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+host+`/jwt/auth",service="container_registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/jwt/auth", func(w http.ResponseWriter, r *http.Request) {
		access := "[]"
		if r.URL.Query().Get("scope") == "repository:devops/api:pull" {
			access = `[{"type":"repository","name":"devops/api","actions":["pull"]}]`
		}
		if r.URL.Query().Get("scope") == "repository:opaque/app:pull" {
			w.Write([]byte(`{"token":"opaque-not-a-jwt"}`))
			return
		}
		w.Write([]byte(`{"token":"` + jwt(access) + `"}`))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	host = strings.TrimPrefix(srv.URL, "https://")
	ctx := context.Background()
	c := Credential{Prefix: host + "/devops", Username: "u", Password: "pw"}
	if r := TestLogin(ctx, srv.Client(), c, "devops/api"); r.Outcome != LoginOK || !strings.Contains(r.Message, "read access to devops/api") {
		t.Fatalf("granted: %+v", r)
	}
	if r := TestLogin(ctx, srv.Client(), c, "platform/tools"); r.Outcome != LoginRejected || !strings.Contains(r.Message, "no read access to platform/tools") {
		t.Fatalf("other group: %+v", r)
	}
	if r := TestLogin(ctx, srv.Client(), c, "opaque/app"); r.Outcome != LoginOK { // not a JWT: can't tell, trust the 200
		t.Fatalf("opaque token: %+v", r)
	}
	if r := TestLogin(ctx, srv.Client(), c, ""); r.Outcome != LoginOK || !strings.Contains(r.Message, "not checked") {
		t.Fatalf("no repository: %+v", r)
	}
}
