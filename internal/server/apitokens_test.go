package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"owl/internal/auth"
	"owl/internal/store"
)

func asViewer(t *testing.T, base string) *http.Client {
	c := browser(t)
	c.Jar.SetCookies(mustURL(base), []*http.Cookie{{Name: "owl_session", Value: testViewerToken}})
	return c
}

// newToken creates a token through the API (as a browser session) and returns its secret and id.
func newToken(t *testing.T, c *http.Client, base, body string) (string, int64) {
	t.Helper()
	code, out := doJSON(t, c, "POST", base+"/api/v1/tokens", body)
	if code != http.StatusCreated {
		t.Fatalf("create token %s: %d %v", body, code, out)
	}
	secret, _ := out["secret"].(string)
	tok, _ := out["token"].(map[string]any)
	if !auth.IsAPIToken(secret) || tok == nil {
		t.Fatalf("bad create answer: %v", out)
	}
	return secret, int64(tok["id"].(float64))
}

func bearer(secret string) []string { return []string{"Authorization", "Bearer " + secret} }

func TestAPITokenLifecycle(t *testing.T) {
	e := newSigninEnv(t)
	base := e.srv.URL
	admin := asAdmin(t, base)
	cli := &http.Client{} // no cookie jar: like the CLI

	read, _ := newToken(t, admin, base, `{"name":"laptop"}`)
	write, writeID := newToken(t, admin, base, `{"name":"gitlab-ci","scope":"write","expiresDays":0}`)

	// Default: read scope, 90 days.
	ts, _ := e.db.ListAPITokens(context.Background(), 0)
	if len(ts) != 2 || ts[1].Name != "laptop" || ts[1].Scope != store.ScopeRead || ts[1].ExpiresAt == nil ||
		ts[1].ExpiresAt.Sub(time.Now()) < 89*24*time.Hour || ts[0].ExpiresAt != nil {
		t.Fatalf("stored tokens: %+v", ts)
	}
	// Only the hash is stored.
	if _, _, err := e.db.APITokenByHash(context.Background(), read); err == nil {
		t.Fatal("token stored in clear")
	}

	// Reads work with either token; /auth/me says which token and its effective scope.
	if code, me := doJSON(t, cli, "GET", base+"/api/v1/auth/me", "", bearer(read)...); code != 200 ||
		me["email"] != "admin@example.com" || me["token"].(map[string]any)["scope"] != "read" {
		t.Fatalf("me with read token: %d %v", code, me)
	}
	if code, _ := doJSON(t, cli, "GET", base+"/api/v1/clusters", "", bearer(read)...); code != 200 {
		t.Fatalf("read with token: %d", code)
	}

	// A read token can't write; a write token can — no Origin/cookie needed — and the audit names it.
	body := `{"name":"it-new","stage":"staging","tenant":"main"}`
	if code, _ := doJSON(t, cli, "POST", base+"/api/v1/clusters", body, bearer(read)...); code != 403 {
		t.Fatalf("read token wrote: %d", code)
	}
	if code, out := doJSON(t, cli, "POST", base+"/api/v1/clusters", body, append(bearer(write), "Origin", "https://evil.example")...); code != 201 {
		t.Fatalf("write token: %d %v", code, out)
	}
	audit, _ := e.db.ListAudit(context.Background(), 10)
	if len(audit) == 0 || audit[0].Actor != "admin@example.com (token: gitlab-ci)" {
		t.Fatalf("audit actor: %+v", audit)
	}

	// Tokens never touch sign-in or tokens, whatever the scope.
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/tokens", `{"name":"x"}`},
		{"DELETE", "/api/v1/tokens/" + itoa(writeID), ""},
		{"POST", "/api/v1/auth/password", `{"current":"a","new":"bbbbbbbbbbbbbbbb"}`},
		{"POST", "/api/v1/auth/logout", ""},
		{"PUT", "/api/v1/auth/settings", `{"passwordLogin":true}`},
		{"PUT", "/api/v1/auth/digest-email", `{"on":true}`},
		{"GET", "/api/v1/auth/settings", ""},
	} {
		if code, _ := doJSON(t, cli, c.method, base+c.path, c.body, bearer(write)...); code != 403 {
			t.Errorf("%s %s with a token: %d, want 403", c.method, c.path, code)
		}
	}

	// Cookie and token together: refused.
	if code, _ := doJSON(t, admin, "GET", base+"/api/v1/clusters", "", bearer(read)...); code != 400 {
		t.Fatalf("cookie + token: %d", code)
	}

	// owl logout: the token revokes itself and stops working.
	if code, _ := doJSON(t, cli, "DELETE", base+"/api/v1/tokens/current", "", bearer(read)...); code != 204 {
		t.Fatalf("self revoke: %d", code)
	}
	if code, _ := doJSON(t, cli, "GET", base+"/api/v1/clusters", "", bearer(read)...); code != 401 {
		t.Fatalf("revoked token still works: %d", code)
	}
	// Only a token can revoke "current".
	if code, _ := doJSON(t, admin, "DELETE", base+"/api/v1/tokens/current", ""); code != 400 {
		t.Fatalf("session revoking current: %d", code)
	}
}

func TestAPITokenScopeFollowsRole(t *testing.T) {
	e := newSigninEnv(t)
	base := e.srv.URL
	cli := &http.Client{}
	ctx := context.Background()

	// Viewers can't create write tokens.
	if code, _ := doJSON(t, asViewer(t, base), "POST", base+"/api/v1/tokens", `{"name":"x","scope":"write"}`); code != 403 {
		t.Fatalf("viewer write token: %d", code)
	}
	viewerTok, _ := newToken(t, asViewer(t, base), base, `{"name":"x"}`)
	if code, _ := doJSON(t, cli, "GET", base+"/api/v1/users", "", bearer(viewerTok)...); code != 403 {
		t.Fatalf("viewer token on admin read: %d", code)
	}

	// An admin's write token becomes read-only when the admin is demoted, and dies when disabled.
	write, _ := newToken(t, asAdmin(t, base), base, `{"name":"ci","scope":"write"}`)
	admin, _ := e.db.UserByEmail(ctx, "admin@example.com")
	other, _ := e.db.CreateUser(ctx, "other@example.com", store.RoleAdmin)
	_ = e.db.SetPassword(ctx, other.ID, "$argon2id$unused", "")
	if _, err := e.db.UpdateUser(ctx, admin.ID, store.RoleViewer, false); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"it-new","stage":"staging","tenant":"main"}`
	if code, _ := doJSON(t, cli, "POST", base+"/api/v1/clusters", body, bearer(write)...); code != 403 {
		t.Fatalf("demoted admin's write token wrote: %d", code)
	}
	if _, me := doJSON(t, cli, "GET", base+"/api/v1/auth/me", "", bearer(write)...); me["token"].(map[string]any)["scope"] != "read" {
		t.Fatalf("effective scope: %v", me)
	}
	if _, err := e.db.UpdateUser(ctx, admin.ID, store.RoleViewer, true); err != nil {
		t.Fatal(err)
	}
	if code, _ := doJSON(t, cli, "GET", base+"/api/v1/clusters", "", bearer(write)...); code != 401 {
		t.Fatalf("disabled user's token: %d", code)
	}
}

func TestAPITokenValidationAndRevoke(t *testing.T) {
	e := newSigninEnv(t)
	base := e.srv.URL
	admin, viewer := asAdmin(t, base), asViewer(t, base)
	for _, b := range []string{`{"name":""}`, `{"name":" x"}`, `{"name":"a/b"}`, `{"name":"` + strings.Repeat("a", 41) + `"}`,
		`{"name":"x","scope":"root"}`, `{"name":"x","expiresDays":7}`} {
		if code, _ := doJSON(t, admin, "POST", base+"/api/v1/tokens", b); code != 400 {
			t.Errorf("%s: %d, want 400", b, code)
		}
	}
	newToken(t, admin, base, `{"name":"dup"}`)
	if code, _ := doJSON(t, admin, "POST", base+"/api/v1/tokens", `{"name":"dup"}`); code != 409 {
		t.Fatalf("duplicate name: %d", code)
	}
	// A viewer sees and revokes only their own; an admin revokes anyone's.
	vt, vid := newToken(t, viewer, base, `{"name":"mine"}`)
	_, aid := newToken(t, admin, base, `{"name":"admins"}`)
	if code, _ := doJSON(t, viewer, "DELETE", base+"/api/v1/tokens/"+itoa(aid), ""); code != 404 {
		t.Fatalf("viewer revoked admin's token: %d", code)
	}
	if code, _ := doJSON(t, viewer, "GET", base+"/api/v1/tokens/all", ""); code != 403 {
		t.Fatalf("viewer listed all tokens: %d", code)
	}
	if code, _ := doJSON(t, admin, "DELETE", base+"/api/v1/tokens/"+itoa(vid), ""); code != 204 {
		t.Fatalf("admin revoke: %d", code)
	}
	if code, _ := doJSON(t, &http.Client{}, "GET", base+"/api/v1/report", "", bearer(vt)...); code != 401 {
		t.Fatalf("revoked by admin still works: %d", code)
	}
}

func TestAPITokenExpiryAndFailures(t *testing.T) {
	e := newSigninEnv(t)
	base := e.srv.URL
	ctx := context.Background()
	admin, _ := e.db.UserByEmail(ctx, "admin@example.com")
	secret, hash, prefix := auth.NewAPIToken()
	past := time.Now().Add(-time.Minute)
	if _, err := e.db.CreateAPIToken(ctx, store.APIToken{UserID: admin.ID, Name: "old", Prefix: prefix, Scope: store.ScopeRead, ExpiresAt: &past}, hash); err != nil {
		t.Fatal(err)
	}
	if code, out := doJSON(t, &http.Client{}, "GET", base+"/api/v1/report", "", bearer(secret)...); code != 401 || !strings.Contains(out["error"].(string), "expired") {
		t.Fatalf("expired token: %d %v", code, out)
	}

	// Expiry warning: once, a week before, by email.
	_, hash2, prefix2 := auth.NewAPIToken()
	soon := time.Now().Add(3 * 24 * time.Hour)
	if _, err := e.db.CreateAPIToken(ctx, store.APIToken{UserID: admin.ID, Name: "soon", Prefix: prefix2, Scope: store.ScopeRead, ExpiresAt: &soon}, hash2); err != nil {
		t.Fatal(err)
	}
	e.app.warnExpiringTokens(ctx)
	e.app.warnExpiringTokens(ctx)
	msgs := e.mail.all()
	if len(msgs) != 1 || msgs[0].To != "admin@example.com" || !strings.Contains(msgs[0].Subject, `"soon"`) || !strings.Contains(msgs[0].Body, base+"/account") {
		t.Fatalf("expiry mails: %+v", msgs)
	}

	// Garbage tokens count as failures per IP (the expired one above was the first): after the limit,
	// 429 even before a lookup.
	for i := 1; i < apiTokenFailures; i++ {
		if code, _ := doJSON(t, &http.Client{}, "GET", base+"/api/v1/report", "", "Authorization", "Bearer owl_pat_nope"); code != 401 {
			t.Fatalf("bad token %d: %d", i, code)
		}
	}
	if code, _ := doJSON(t, &http.Client{}, "GET", base+"/api/v1/report", "", "Authorization", "Bearer owl_pat_nope"); code != 429 {
		t.Fatalf("after failures: %d", code)
	}
}

func TestAPITokenReviewFixes(t *testing.T) {
	e := newSigninEnv(t)
	base := e.srv.URL
	ctx := context.Background()
	admin := asAdmin(t, base)
	cli := &http.Client{}
	write, _ := newToken(t, admin, base, `{"name":"ci","scope":"write"}`)

	// A reverse proxy's Basic auth (or an SSO bearer) next to the session cookie doesn't break the UI.
	if code, _ := doJSON(t, admin, "GET", base+"/api/v1/clusters", "", "Authorization", "Basic dXNlcjpwdw=="); code != 200 {
		t.Fatalf("basic auth + cookie: %d", code)
	}
	if code, _ := doJSON(t, admin, "GET", base+"/api/v1/clusters", "", "Authorization", "Bearer eyJhbGciOi.jwt.sig"); code != 200 {
		t.Fatalf("foreign bearer + cookie: %d", code)
	}

	// No sign-in links for existing accounts with a token (account takeover).
	v, _ := e.db.UserByEmail(ctx, "viewer@example.com")
	for _, p := range []string{"/reset", "/invite"} {
		if code, _ := doJSON(t, cli, "POST", base+"/api/v1/users/"+itoa(v.ID)+p, "", bearer(write)...); code != 403 {
			t.Errorf("token POST users/{id}%s: %d", p, code)
		}
	}
	// Inviting a new user with a token: emailed, so the link isn't handed back.
	if code, out := doJSON(t, cli, "POST", base+"/api/v1/users", `{"email":"n@example.com","role":"viewer"}`, bearer(write)...); code != 201 || out["link"] != "" || out["emailed"] != true {
		t.Fatalf("token invite: %d %v", code, out)
	}

	// Someone else's failures don't lock a valid token out.
	for i := 0; i < apiTokenFailures+5; i++ {
		doJSON(t, cli, "GET", base+"/api/v1/report", "", "Authorization", "Bearer owl_pat_nope")
	}
	if code, _ := doJSON(t, cli, "GET", base+"/api/v1/report", "", bearer(write)...); code != 200 {
		t.Fatalf("valid token after others' failures: %d", code)
	}

	// Disabling a user deletes their tokens: enabling again doesn't revive them.
	vt, _ := newToken(t, asViewer(t, base), base, `{"name":"v"}`)
	if _, err := e.db.UpdateUser(ctx, v.ID, store.RoleViewer, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.UpdateUser(ctx, v.ID, store.RoleViewer, false); err != nil {
		t.Fatal(err)
	}
	// (this IP is over the failure limit from above, so a refused token gets 429 rather than 401)
	if code, _ := doJSON(t, cli, "GET", base+"/api/v1/report", "", bearer(vt)...); code != 401 && code != 429 {
		t.Fatalf("token revived by enable: %d", code)
	}
}
