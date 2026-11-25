package scan

import (
	"context"
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
	if r := TestLogin(ctx, srv.Client(), Credential{Prefix: host + "/team-a", Username: "u", Password: "pw"}); r.Outcome != LoginOK || scope != "repository:team-a:pull" {
		t.Fatalf("bearer ok: %+v scope %q", r, scope)
	}
	r := TestLogin(ctx, srv.Client(), Credential{Prefix: host, Username: "u", Password: "wrong-secret"})
	if r.Outcome != LoginRejected || strings.Contains(r.Message, "wrong-secret") {
		t.Fatalf("bearer rejected: %+v", r)
	}

	// Basic challenge.
	srvB, hostB := fakeRegistry(t, func(string) string { return `Basic realm="Registry"` }, ok)
	if r := TestLogin(ctx, srvB.Client(), Credential{Prefix: hostB, Username: "u", Password: "pw"}); r.Outcome != LoginOK {
		t.Fatalf("basic: %+v", r)
	}
	if r := TestLogin(ctx, srvB.Client(), Credential{Prefix: hostB, Username: "u", Password: "x"}); r.Outcome != LoginRejected {
		t.Fatalf("basic rejected: %+v", r)
	}

	// A realm on the metadata service or over plain HTTP: refused, the password never sent there.
	for _, realm := range []string{"https://169.254.169.254/latest", "http://auth.example.com/jwt", "https://localhost/jwt"} {
		srvM, hostM := fakeRegistry(t, func(string) string { return `Bearer realm="` + realm + `"` }, ok)
		if r := TestLogin(ctx, srvM.Client(), Credential{Prefix: hostM, Username: "u", Password: "pw"}); r.Outcome != LoginUnexpected {
			t.Fatalf("realm %s: %+v", realm, r)
		}
	}

	// Not resolvable / not a TLS server.
	if r := TestLogin(ctx, &http.Client{Timeout: 5 * time.Second}, Credential{Prefix: "owl-no-such-host.invalid", Username: "u", Password: "pw"}); r.Outcome != LoginDNS {
		t.Fatalf("dns: %+v", r)
	}
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	if r := TestLogin(ctx, nil, Credential{Prefix: strings.TrimPrefix(plain.URL, "http://"), Username: "u", Password: "pw"}); r.Outcome != LoginTLS {
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
