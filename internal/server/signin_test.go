package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"owl/internal/catalog"
	"owl/internal/mail"
	"owl/internal/store"
	"owl/internal/store/sqlite"
)

type fakeMail struct {
	mu   sync.Mutex
	msgs []mail.Message
	sent chan struct{}
}

func (f *fakeMail) Send(_ context.Context, m mail.Message) error {
	f.mu.Lock()
	f.msgs = append(f.msgs, m)
	f.mu.Unlock()
	select {
	case f.sent <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeMail) all() []mail.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mail.Message(nil), f.msgs...)
}

type signinEnv struct {
	srv  *httptest.Server
	app  *Server
	db   *sqlite.DB
	mail *fakeMail
	logs *bytes.Buffer
}

func newSigninEnv(t *testing.T) *signinEnv {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	addTestUsers(t, db)
	cat, _ := catalog.Builtin()
	fm := &fakeMail{sent: make(chan struct{}, 10)}
	logs := &bytes.Buffer{}
	app := &Server{Store: db, Catalog: cat, Log: slog.New(slog.NewJSONHandler(&syncWriter{w: logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	pub, _ := url.Parse(srv.URL)
	app.Auth = AuthConfig{PublicURL: pub, Mail: fm}
	return &signinEnv{srv: srv, app: app, db: db, mail: fm, logs: logs}
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// browser is an HTTP client with a cookie jar, like a browser tab.
func browser(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func doJSON(t *testing.T, c *http.Client, method, u, body string, hdr ...string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, u, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
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

func asAdmin(t *testing.T, base string) *http.Client {
	c := browser(t)
	u, _ := url.Parse(base)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: "owl_session", Value: testAdminToken}})
	return c
}

func TestSignInRequiredForAPI(t *testing.T) {
	e := newSigninEnv(t)
	anon := browser(t)
	for _, p := range []string{"/api/v1/clusters", "/api/v1/report", "/api/v1/summaries", "/api/v1/auth/me", "/api/v1/nope"} {
		if code, _ := doJSON(t, anon, "GET", e.srv.URL+p, ""); code != 401 {
			t.Errorf("GET %s without sign-in: %d", p, code)
		}
	}
	for _, p := range []string{"/api/v1/auth/config", "/api/v1/agent/manifest", "/healthz", "/", "/login"} {
		if code, _ := doJSON(t, anon, "GET", e.srv.URL+p, ""); code != 200 && !(code == 404 && (p == "/" || p == "/login")) {
			t.Errorf("GET %s must stay public: %d", p, code)
		}
	}
	_, cfg := doJSON(t, anon, "GET", e.srv.URL+"/api/v1/auth/config", "")
	if cfg["password"] != true || cfg["google"] != false || cfg["forgot"] != true {
		t.Errorf("config: %v", cfg)
	}
}

func TestInviteAcceptLoginLogout(t *testing.T) {
	e := newSigninEnv(t)
	admin := asAdmin(t, e.srv.URL)
	code, out := doJSON(t, admin, "POST", e.srv.URL+"/api/v1/users", `{"email":"New.User@Example.com","role":"viewer"}`)
	if code != 201 || out["emailed"] != true {
		t.Fatalf("invite: %d %v", code, out)
	}
	link := out["link"].(string)
	if !strings.HasPrefix(link, e.srv.URL+"/invite#") {
		t.Fatalf("link: %s", link)
	}
	token := link[strings.IndexByte(link, '#')+1:]
	msgs := e.mail.all()
	if len(msgs) != 1 || msgs[0].To != "new.user@example.com" || !strings.Contains(msgs[0].Body, link) || !strings.Contains(msgs[0].Body, "admin@example.com") {
		t.Fatalf("invite email: %+v", msgs)
	}
	if code, _ := doJSON(t, admin, "POST", e.srv.URL+"/api/v1/users", `{"email":"new.user@example.com","role":"viewer"}`); code != 409 {
		t.Errorf("duplicate invite: %d", code)
	}
	for _, bad := range []string{`"Name <x@example.com>"`, `"x@example.com\r\nBcc: y@example.com"`, `"nope"`} {
		if code, _ := doJSON(t, admin, "POST", e.srv.URL+"/api/v1/users", `{"email":`+bad+`,"role":"viewer"}`); code != 400 {
			t.Errorf("bad email %s: %d", bad, code)
		}
	}

	tab := browser(t)
	if code, info := doJSON(t, tab, "POST", e.srv.URL+"/api/v1/auth/token", `{"token":"`+token+`"}`); code != 200 || info["email"] != "new.user@example.com" || info["kind"] != "invite" {
		t.Fatalf("token info: %d %v", code, info)
	}
	if code, _ := doJSON(t, tab, "POST", e.srv.URL+"/api/v1/auth/accept", `{"token":"`+token+`","password":"short"}`); code != 400 {
		t.Errorf("short password: %d", code)
	}
	const pw = "correct horse battery staple"
	if code, me := doJSON(t, tab, "POST", e.srv.URL+"/api/v1/auth/accept", `{"token":"`+token+`","password":"`+pw+`"}`); code != 200 || me["role"] != "viewer" {
		t.Fatalf("accept: %d %v", code, me)
	}
	if code, me := doJSON(t, tab, "GET", e.srv.URL+"/api/v1/auth/me", ""); code != 200 || me["email"] != "new.user@example.com" {
		t.Fatalf("signed in after accepting: %d %v", code, me)
	}
	if code, _ := doJSON(t, browser(t), "POST", e.srv.URL+"/api/v1/auth/accept", `{"token":"`+token+`","password":"`+pw+`"}`); code != 404 {
		t.Errorf("an invite link works once: %d", code)
	}
	if code, _ := doJSON(t, tab, "GET", e.srv.URL+"/api/v1/users", ""); code != 403 {
		t.Errorf("viewers can't list users: %d", code)
	}
	if code, _ := doJSON(t, tab, "POST", e.srv.URL+"/api/v1/auth/logout", `{}`); code != 200 {
		t.Errorf("logout: %d", code)
	}
	if code, _ := doJSON(t, tab, "GET", e.srv.URL+"/api/v1/auth/me", ""); code != 401 {
		t.Errorf("signed out: %d", code)
	}

	// Password sign-in: unknown email and wrong password get the same answer.
	tab2 := browser(t)
	_, a := doJSON(t, tab2, "POST", e.srv.URL+"/api/v1/auth/login", `{"email":"nobody@example.com","password":"`+pw+`"}`)
	_, b := doJSON(t, tab2, "POST", e.srv.URL+"/api/v1/auth/login", `{"email":"new.user@example.com","password":"wrong password here"}`)
	if a["error"] != wrongLogin || b["error"] != wrongLogin {
		t.Errorf("login errors must not reveal accounts: %v / %v", a, b)
	}
	if code, _ := doJSON(t, tab2, "POST", e.srv.URL+"/api/v1/auth/login", `{"email":"NEW.USER@example.com","password":"`+pw+`"}`); code != 200 {
		t.Fatalf("login: %d", code)
	}
	if code, _ := doJSON(t, tab2, "GET", e.srv.URL+"/api/v1/report", ""); code != 200 {
		t.Errorf("signed-in read: %d", code)
	}

	// Disabling the user ends the session at once.
	us, _ := e.db.UserByEmail(context.Background(), "new.user@example.com")
	if code, out := doJSON(t, admin, "PATCH", e.srv.URL+"/api/v1/users/"+itoa(us.ID), `{"role":"viewer","disabled":true}`); code != 200 {
		t.Fatalf("disable: %d %v", code, out)
	}
	if code, _ := doJSON(t, tab2, "GET", e.srv.URL+"/api/v1/report", ""); code != 401 {
		t.Errorf("disabled user's session must end: %d", code)
	}

	logs := e.logs.String()
	for _, secret := range []string{pw, token, testAdminToken} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs contain a secret %q", secret)
		}
	}
	audit, _ := e.db.ListAudit(context.Background(), 20)
	var actions []string
	for _, a := range audit {
		actions = append(actions, a.Action+"/"+a.Actor)
	}
	want := "user disabled/admin@example.com invite accepted/new.user@example.com user invited/admin@example.com"
	if strings.Join(actions, " ") != want {
		t.Errorf("audit:\n got %s\nwant %s", strings.Join(actions, " "), want)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestLoginRateLimit(t *testing.T) {
	e := newSigninEnv(t)
	c := browser(t)
	codes := map[int]int{}
	for i := 0; i < 11; i++ {
		code, _ := doJSON(t, c, "POST", e.srv.URL+"/api/v1/auth/login", `{"email":"admin@example.com","password":"not the password!"}`)
		codes[code]++
	}
	if codes[401] != 10 || codes[429] != 1 {
		t.Errorf("10 failures then 429: %v", codes)
	}
	// Failing on purpose from one IP must not lock the account out for everyone else.
	e.app.Auth.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	if code, _ := doJSON(t, c, "POST", e.srv.URL+"/api/v1/auth/login", `{"email":"admin@example.com","password":"not the password!"}`,
		"X-Forwarded-For", "203.0.113.7"); code != 401 {
		t.Errorf("another IP must still be able to try: %d", code)
	}
}

// A database error while looking up the session is a 503 and must not clear the cookie.
func TestSessionLookupErrorKeepsCookie(t *testing.T) {
	e := newSigninEnv(t)
	e.db.Close()
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/report", nil)
	withSession(req, testAdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 || len(resp.Cookies()) != 0 {
		t.Errorf("got %d, cookies %v", resp.StatusCode, resp.Cookies())
	}
}

func TestAdminGuards(t *testing.T) {
	e := newSigninEnv(t)
	admin := asAdmin(t, e.srv.URL)
	me, _ := e.db.UserByEmail(context.Background(), "admin@example.com")
	viewer, _ := e.db.UserByEmail(context.Background(), "viewer@example.com")
	if code, _ := doJSON(t, admin, "PATCH", e.srv.URL+"/api/v1/users/"+itoa(me.ID), `{"role":"viewer","disabled":false}`); code != 409 {
		t.Errorf("demote yourself: %d", code)
	}
	if code, _ := doJSON(t, admin, "DELETE", e.srv.URL+"/api/v1/users/"+itoa(me.ID), ""); code != 409 {
		t.Errorf("delete yourself: %d", code)
	}
	if code, _ := doJSON(t, admin, "POST", e.srv.URL+"/api/v1/users/"+itoa(viewer.ID)+"/invite", ""); code != 409 {
		t.Errorf("re-invite an active user: %d", code)
	}
	if code, out := doJSON(t, admin, "POST", e.srv.URL+"/api/v1/users/"+itoa(viewer.ID)+"/reset", ""); code != 200 || !strings.Contains(out["link"].(string), "/reset#") {
		t.Errorf("reset link: %d %v", code, out)
	}
	// Password sign-in can't be turned off without Google and a Google-linked admin.
	if code, _ := doJSON(t, admin, "PUT", e.srv.URL+"/api/v1/auth/settings", `{"passwordLogin":false}`); code != 409 {
		t.Errorf("turn off passwords without Google: %d", code)
	}
	// Cross-origin writes are refused even with a valid session (CSRF).
	if code, _ := doJSON(t, admin, "POST", e.srv.URL+"/api/v1/users", `{"email":"x@example.com","role":"admin"}`, "Origin", "https://evil.example.com"); code != 403 {
		t.Errorf("cross-origin write: %d", code)
	}
	if code, _ := doJSON(t, admin, "POST", e.srv.URL+"/api/v1/users", `{"email":"x@example.com","role":"admin"}`, "Sec-Fetch-Site", "cross-site"); code != 403 {
		t.Errorf("cross-site write: %d", code)
	}
	if code, _ := doJSON(t, admin, "POST", e.srv.URL+"/api/v1/users", `{"email":"x@example.com","role":"admin"}`, "Sec-Fetch-Site", "same-origin"); code != 201 {
		t.Errorf("same-origin write: %d", code)
	}
}

func TestForgotPassword(t *testing.T) {
	e := newSigninEnv(t)
	c := browser(t)
	code, a := doJSON(t, c, "POST", e.srv.URL+"/api/v1/auth/forgot", `{"email":"viewer@example.com"}`)
	select {
	case <-e.mail.sent:
	case <-time.After(5 * time.Second):
		t.Fatal("no reset email")
	}
	_, b := doJSON(t, c, "POST", e.srv.URL+"/api/v1/auth/forgot", `{"email":"nobody@example.com"}`)
	if code != 200 || a["message"] != b["message"] {
		t.Errorf("same answer for known and unknown: %v / %v", a, b)
	}
	time.Sleep(100 * time.Millisecond)
	msgs := e.mail.all()
	if len(msgs) != 1 || msgs[0].To != "viewer@example.com" || !strings.Contains(msgs[0].Body, e.srv.URL+"/reset#") {
		t.Fatalf("reset emails: %+v", msgs)
	}
	link := msgs[0].Body[strings.Index(msgs[0].Body, "/reset#")+7:]
	token := strings.Fields(link)[0]
	if code, _ := doJSON(t, c, "POST", e.srv.URL+"/api/v1/auth/accept", `{"token":"`+token+`","password":"a brand new password"}`); code != 200 {
		t.Fatalf("reset: %d", code)
	}
	// The viewer's old session ended with the new password.
	old := asAdmin(t, e.srv.URL)
	old.Jar.SetCookies(mustURL(e.srv.URL), []*http.Cookie{{Name: "owl_session", Value: testViewerToken}})
	if code, _ := doJSON(t, old, "GET", e.srv.URL+"/api/v1/auth/me", ""); code != 401 {
		t.Errorf("old session after reset: %d", code)
	}
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

// fakeGoogle answers the token endpoint with an ID token carrying the claims the test sets.
type fakeGoogle struct {
	srv    *httptest.Server
	claims map[string]any
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	f := &fakeGoogle{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_secret") != "secret" || r.Form.Get("code") != "the-code" || r.Form.Get("code_verifier") == "" {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		payload, _ := json.Marshal(f.claims)
		tok := "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": tok, "access_token": "x"})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestGoogleSignIn(t *testing.T) {
	e := newSigninEnv(t)
	fg := newFakeGoogle(t)
	g, err := NewGoogleConfig("client-id", "secret", "example.com", e.app.Auth.PublicURL)
	if err != nil {
		t.Fatal(err)
	}
	g.AuthURL, g.TokenURL = "https://accounts.example.test/auth", fg.srv.URL
	e.app.Auth.Google = g
	ctx := context.Background()
	invited, _ := e.db.CreateUser(ctx, "g@example.com", store.RoleAdmin)

	signIn := func(claims map[string]any) (*http.Client, string) {
		c := browser(t)
		resp, err := c.Get(e.srv.URL + "/auth/google/start")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		loc, _ := url.Parse(resp.Header.Get("Location"))
		q := loc.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("hd") != "example.com" || q.Get("redirect_uri") != e.srv.URL+"/auth/google/callback" {
			t.Fatalf("auth redirect: %s", loc)
		}
		for k, v := range claims {
			if v == "$nonce" {
				claims[k] = q.Get("nonce")
			}
		}
		fg.claims = claims
		resp, err = c.Get(e.srv.URL + "/auth/google/callback?state=" + url.QueryEscape(q.Get("state")) + "&code=the-code")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return c, resp.Header.Get("Location")
	}
	good := func() map[string]any {
		return map[string]any{"iss": "https://accounts.google.com", "aud": "client-id", "exp": time.Now().Add(time.Hour).Unix(),
			"nonce": "$nonce", "sub": "sub-g", "email": "G@example.com", "email_verified": true, "hd": "example.com"}
	}
	for name, mod := range map[string]func(map[string]any){
		"wrong nonce":    func(c map[string]any) { c["nonce"] = "other" },
		"wrong audience": func(c map[string]any) { c["aud"] = "someone-else" },
		"unverified":     func(c map[string]any) { c["email_verified"] = false },
		"other domain":   func(c map[string]any) { c["hd"] = "gmail.com" },
		"expired":        func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() },
		"not invited":    func(c map[string]any) { c["email"] = "stranger@example.com"; c["sub"] = "sub-x" },
	} {
		c := good()
		mod(c)
		if _, loc := signIn(c); !strings.HasPrefix(loc, "/login?error=") {
			t.Errorf("%s: signed in (%s)", name, loc)
		}
	}
	c, loc := signIn(good())
	if loc != "/" {
		t.Fatalf("google sign-in: %s", loc)
	}
	if code, me := doJSON(t, c, "GET", e.srv.URL+"/api/v1/auth/me", ""); code != 200 || me["email"] != "g@example.com" || me["hasGoogle"] != true {
		t.Fatalf("me: %d %v", code, me)
	}
	if u, _ := e.db.GetUser(ctx, invited.ID); !u.Active() || u.GoogleSub != "sub-g" {
		t.Errorf("first Google sign-in accepts the invite and links the account: %+v", u)
	}
	// Another Google account with the same email can't take the linked user over.
	other := good()
	other["sub"] = "sub-other"
	if _, loc := signIn(other); loc != "/login?error=account_mismatch" {
		t.Errorf("different Google account: %s", loc)
	}
	// A callback that wasn't started in this browser is refused (login CSRF).
	resp, _ := browser(t).Get(e.srv.URL + "/auth/google/callback?state=forged&code=the-code")
	if resp.Header.Get("Location") != "/login?error=state" {
		t.Errorf("forged state: %s", resp.Header.Get("Location"))
	}
	// Now password sign-in can be turned off, and password login is refused.
	admin := asAdmin(t, e.srv.URL)
	if code, out := doJSON(t, admin, "PUT", e.srv.URL+"/api/v1/auth/settings", `{"passwordLogin":false}`); code != 200 || out["passwordLogin"] != false {
		t.Fatalf("turn off passwords: %d %v", code, out)
	}
	if code, _ := doJSON(t, browser(t), "POST", e.srv.URL+"/api/v1/auth/login", `{"email":"admin@example.com","password":"whatever-whatever"}`); code != 403 {
		t.Errorf("password login while off: %d", code)
	}
	if code, _ := doJSON(t, browser(t), "POST", e.srv.URL+"/api/v1/auth/forgot", `{"email":"admin@example.com"}`); code != 404 {
		t.Errorf("forgot while off: %d", code)
	}
	// The last admin who can sign in with Google can't be demoted, disabled or deleted while passwords are off.
	id := itoa(invited.ID)
	for _, req := range []struct{ method, body string }{{"PATCH", `{"role":"viewer","disabled":false}`}, {"PATCH", `{"role":"admin","disabled":true}`}, {"DELETE", ""}} {
		if code, _ := doJSON(t, admin, req.method, e.srv.URL+"/api/v1/users/"+id, req.body); code != 409 {
			t.Errorf("%s %s on the last Google admin: %d", req.method, req.body, code)
		}
	}
	// Google settings gone (env changed): passwords are back on rather than nobody being able to sign in.
	e.app.Auth.Google = nil
	if _, cfg := doJSON(t, browser(t), "GET", e.srv.URL+"/api/v1/auth/config", ""); cfg["password"] != true {
		t.Errorf("password sign-in must come back without Google: %v", cfg)
	}
}

func TestClientIP(t *testing.T) {
	s := &Server{Auth: AuthConfig{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.30.80.0/24")}}}
	for _, c := range []struct{ remote, xff, want string }{
		{"203.0.113.9:1234", "1.2.3.4", "203.0.113.9"},              // untrusted peer: XFF ignored
		{"172.30.80.2:1234", "1.2.3.4", "1.2.3.4"},                  // via the proxy
		{"172.30.80.2:1234", "6.6.6.6, 1.2.3.4", "1.2.3.4"},         // left entries are client-supplied
		{"172.30.80.2:1234", "1.2.3.4, 172.30.80.3", "1.2.3.4"},     // chained trusted proxies
		{"172.30.80.2:1234", "", "172.30.80.2"},                     // no header
		{"172.30.80.2:1234", "garbage", "172.30.80.2"},              // malformed
		{"[::ffff:172.30.80.2]:1234", "2001:db8::1", "2001:db8::1"}, // IPv4-mapped peer
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := s.clientIP(r); got != c.want {
			t.Errorf("%s / %q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestSessionCookieFlags(t *testing.T) {
	for _, c := range []struct {
		host, name string
		secure     bool
	}{{"127.0.0.1:8080", "owl_session", false}, {"localhost", "owl_session", false}, {"owl.example.com", "__Host-owl_session", true}} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = c.host
		setCookie(rec, r, "session", "v", time.Hour)
		ck := rec.Result().Cookies()[0]
		if ck.Name != c.name || ck.Secure != c.secure || !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode || ck.Path != "/" {
			t.Errorf("%s: %+v", c.host, ck)
		}
	}
}
