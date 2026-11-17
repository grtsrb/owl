package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"owl/internal/auth"
	"owl/internal/store"
)

// GoogleConfig is Google sign-in (OpenID Connect, authorization code flow with PKCE).
type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	Domain       string // optional: only accounts of this Google Workspace domain (the hd claim)
	RedirectURL  string // <public URL>/auth/google/callback, registered in the Google Cloud console
	// Endpoints; tests point them at a fake.
	AuthURL  string
	TokenURL string
	HTTP     *http.Client

	mu      sync.Mutex
	pending map[string]oauthPending // state -> what the callback must match
}

const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
	oauthTTL       = 10 * time.Minute
)

type oauthPending struct {
	nonce    string
	verifier string
	expires  time.Time
}

// NewGoogleConfig validates the settings; publicURL must be https (or http://localhost).
func NewGoogleConfig(clientID, secret, domain string, publicURL *url.URL) (*GoogleConfig, error) {
	if clientID == "" && secret == "" {
		return nil, nil
	}
	if clientID == "" || secret == "" {
		return nil, errors.New("set both OWL_GOOGLE_CLIENT_ID and OWL_GOOGLE_CLIENT_SECRET")
	}
	if publicURL == nil {
		return nil, errors.New("Google sign-in needs OWL_PUBLIC_URL (its callback address is registered with Google)")
	}
	return &GoogleConfig{ClientID: clientID, ClientSecret: secret, Domain: strings.ToLower(strings.TrimSpace(domain)),
		RedirectURL: strings.TrimRight(publicURL.String(), "/") + "/auth/google/callback",
		AuthURL:     googleAuthURL, TokenURL: googleTokenURL, HTTP: &http.Client{Timeout: 15 * time.Second}}, nil
}

func randomURLSafe() string {
	s, _ := auth.NewSecret()
	return s
}

// googleStart sends the browser to Google. state (also in a short-lived cookie) ties the callback to
// this browser — no login CSRF; nonce ties the ID token to this request; PKCE ties the code to us.
func (s *Server) googleStart(w http.ResponseWriter, r *http.Request) {
	g := s.Auth.Google
	if g == nil {
		http.Redirect(w, r, "/login?error=google_off", http.StatusFound)
		return
	}
	if !s.limits.allow("google-ip:"+s.limitIP(r), 60, 15*time.Minute, true) {
		http.Redirect(w, r, "/login?error=too_many", http.StatusFound)
		return
	}
	state, nonce, verifier := randomURLSafe(), randomURLSafe(), randomURLSafe()
	g.mu.Lock()
	now := time.Now()
	if g.pending == nil {
		g.pending = map[string]oauthPending{}
	}
	for k, p := range g.pending {
		if now.After(p.expires) {
			delete(g.pending, k)
		}
	}
	if len(g.pending) > 5000 {
		g.mu.Unlock()
		http.Redirect(w, r, "/login?error=too_many", http.StatusFound)
		return
	}
	g.pending[state] = oauthPending{nonce: nonce, verifier: verifier, expires: now.Add(oauthTTL)}
	g.mu.Unlock()
	setCookie(w, r, "oauth", state, oauthTTL)

	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id": {g.ClientID}, "redirect_uri": {g.RedirectURL}, "response_type": {"code"},
		"scope": {"openid email"}, "state": {state}, "nonce": {nonce}, "prompt": {"select_account"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	if g.Domain != "" {
		q.Set("hd", g.Domain) // a hint for the account chooser; the hd claim is what's enforced
	}
	http.Redirect(w, r, g.AuthURL+"?"+q.Encode(), http.StatusFound)
}

type idClaims struct {
	Iss           string `json:"iss"`
	Aud           any    `json:"aud"` // string, or a list in general OIDC
	Exp           int64  `json:"exp"`
	Nonce         string `json:"nonce"`
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified any    `json:"email_verified"` // bool (Google sometimes documented it as "true")
	Hd            string `json:"hd"`
}

func (s *Server) googleCallback(w http.ResponseWriter, r *http.Request) {
	g := s.Auth.Google
	fail := func(code string, args ...any) {
		s.Log.Warn("google sign-in refused", append(args, "reason", code, "remote", s.clientIP(r), "req", requestID(r))...)
		http.Redirect(w, r, "/login?error="+code, http.StatusFound)
	}
	if g == nil {
		fail("google_off")
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	c, err := r.Cookie(cookieName(r, "oauth"))
	setCookie(w, r, "oauth", "", -1)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		fail("state") // not started from this browser, or took longer than 10 minutes
		return
	}
	g.mu.Lock()
	p, ok := g.pending[state]
	delete(g.pending, state)
	g.mu.Unlock()
	if !ok || time.Now().After(p.expires) {
		fail("state")
		return
	}
	if e := q.Get("error"); e != "" {
		fail("google_cancelled", "googleError", e)
		return
	}
	claims, err := g.exchange(r.Context(), q.Get("code"), p.verifier)
	if err != nil {
		fail("google_failed", "err", err)
		return
	}
	if err := g.checkClaims(claims, p.nonce); err != nil {
		fail("google_failed", "err", err)
		return
	}
	email := strings.ToLower(claims.Email)
	if g.Domain != "" && strings.ToLower(claims.Hd) != g.Domain {
		fail("domain", "user", email)
		return
	}
	u, err := s.Store.UserByGoogleSub(r.Context(), claims.Sub)
	if errors.Is(err, store.ErrNotFound) {
		// First Google sign-in: match the invited email, then remember the Google account (sub), so a
		// later change of the account's email can't move it onto someone else's Owl user.
		u, err = s.Store.UserByEmail(r.Context(), email)
		if errors.Is(err, store.ErrNotFound) {
			fail("not_invited", "user", email)
			return
		}
		if err == nil && u.GoogleSub != "" {
			fail("account_mismatch", "user", email) // that Owl user is linked to another Google account
			return
		}
		if err == nil && u.DisabledAt == nil {
			if err = s.Store.LinkGoogle(r.Context(), u.ID, claims.Sub); err == nil {
				u, err = s.Store.GetUser(r.Context(), u.ID)
				if err == nil {
					rr := r.WithContext(context.WithValue(r.Context(), ctxSessionKey{}, &store.Session{User: *u}))
					s.audit(rr, "google account linked", "user", u.Email)
				}
			}
		}
	}
	if err != nil {
		s.Log.Error("google sign-in failed", "err", err)
		fail("google_failed")
		return
	}
	if !u.Active() {
		fail("disabled", "user", u.Email)
		return
	}
	if err := s.startSession(w, r, u, "google"); err != nil {
		s.Log.Error("google sign-in failed", "err", err)
		fail("google_failed")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// exchange trades the code for tokens at Google's token endpoint and returns the ID token's claims.
// The ID token's signature isn't checked: it comes straight from Google's token endpoint over TLS in
// a request authenticated with our client secret, which OpenID Connect Core §3.1.3.7 accepts in place
// of signature validation (Google's docs say the same). Its claims are still all checked.
func (g *GoogleConfig) exchange(ctx context.Context, code, verifier string) (*idClaims, error) {
	if code == "" || len(code) > 2048 {
		return nil, errors.New("missing code")
	}
	form := url.Values{"code": {code}, "client_id": {g.ClientID}, "client_secret": {g.ClientSecret},
		"redirect_uri": {g.RedirectURL}, "grant_type": {"authorization_code"}, "code_verifier": {verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := g.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token endpoint: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return nil, fmt.Errorf("token endpoint: HTTP %d %s", res.StatusCode, e.Error)
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.IDToken == "" {
		return nil, errors.New("token endpoint: no id_token")
	}
	parts := strings.Split(tok.IDToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("malformed id_token payload")
	}
	var c idClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, errors.New("malformed id_token claims")
	}
	return &c, nil
}

func (g *GoogleConfig) checkClaims(c *idClaims, nonce string) error {
	if c.Iss != "https://accounts.google.com" && c.Iss != "accounts.google.com" {
		return fmt.Errorf("issuer %q", c.Iss)
	}
	audOK := false
	switch a := c.Aud.(type) {
	case string:
		audOK = a == g.ClientID
	case []any:
		audOK = len(a) == 1 && a[0] == g.ClientID
	}
	if !audOK {
		return errors.New("audience is not this client")
	}
	if time.Now().Unix() >= c.Exp {
		return errors.New("id_token expired")
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1 {
		return errors.New("nonce mismatch")
	}
	if c.Sub == "" || c.Email == "" {
		return errors.New("no subject or email")
	}
	if v := c.EmailVerified; v != true && v != "true" {
		return errors.New("email not verified by Google")
	}
	return nil
}
