package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"owl/internal/auth"
	"owl/internal/mail"
	"owl/internal/snapshot"
	"owl/internal/store"
)

// Sign-in (milestone 9): invite-only users, a session cookie, password and/or Google sign-in.
const (
	sessionIdle     = 7 * 24 * time.Hour  // signed out after a week without use
	sessionAbsolute = 30 * 24 * time.Hour // and after 30 days whatever happens
	sessionTouch    = 5 * time.Minute     // last-seen is written at most this often
	InviteTTL       = 72 * time.Hour
	ResetTTL        = time.Hour

	settingPasswordLogin = "authPasswordLogin" // "off" disables password sign-in; auth* settings survive restores
)

// AuthConfig is the sign-in configuration that comes from the environment.
type AuthConfig struct {
	// PublicURL is where users reach Owl (https://owl.example.com). Used for links in emails, the
	// Google redirect URL, and allowed automatically as a Host. Optional for local use.
	PublicURL *url.URL
	Google    *GoogleConfig // nil = Google sign-in off
	Mail      mail.Sender   // nil = no email: admins copy links themselves
	MailInfo  mail.Status
	// TrustedProxies may set X-Forwarded-For (the reverse proxy in front of Owl). Without it, the
	// client IP is the TCP peer — behind a proxy that's the proxy, for every user.
	TrustedProxies []netip.Prefix
}

type ctxSessionKey struct{}

// sessionFrom returns the signed-in session of the request, or nil.
func sessionFrom(r *http.Request) *store.Session {
	s, _ := r.Context().Value(ctxSessionKey{}).(*store.Session)
	return s
}

// publicAPI are the /api paths that work without signing in.
var publicAPI = map[string]bool{
	snapshot.IngestPath:      true, // agents: per-cluster bearer token
	"/api/v1/agent/manifest": true, // no secrets; `kubectl apply -f <url>` stays convenient
	"/api/v1/auth/config":    true,
	"/api/v1/auth/login":     true,
	"/api/v1/auth/logout":    true,
	"/api/v1/auth/token":     true,
	"/api/v1/auth/accept":    true,
	"/api/v1/auth/forgot":    true,
}

// loopbackHost: requests to these hosts are local development, where the session cookie can't be
// Secure (plain http://localhost).
func loopbackHost(r *http.Request) bool {
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// cookieName: the __Host- prefix makes the browser insist on Secure, Path=/ and no Domain, so another
// subdomain can't plant or overwrite the cookie.
func cookieName(r *http.Request, base string) string {
	if loopbackHost(r) {
		return "owl_" + base
	}
	return "__Host-owl_" + base
}

func setCookie(w http.ResponseWriter, r *http.Request, base, value string, maxAge time.Duration) {
	c := &http.Cookie{Name: cookieName(r, base), Value: value, Path: "/", HttpOnly: true,
		Secure: !loopbackHost(r), SameSite: http.SameSiteLaxMode, MaxAge: int(maxAge / time.Second)}
	if maxAge < 0 {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// authn loads the session (if any), requires one for the API (except publicAPI), and rejects
// cross-origin writes (net/http's CrossOriginProtection: Sec-Fetch-Site, else Origin vs Host).
func (s *Server) authn(mux *http.ServeMux) http.Handler {
	var next http.Handler = mux
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpError(w, http.StatusForbidden, "cross-origin request refused")
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == snapshot.IngestPath || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r) // agents and probes: no cookies, no browser
			return
		}
		api := r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/")
		if api && isAPIBearer(r) {
			// Personal API tokens (CLI, CI): no cookie, so no cross-site request can carry one and
			// cross-origin protection doesn't apply.
			if _, err := r.Cookie(cookieName(r, "session")); err == nil {
				httpError(w, http.StatusBadRequest, "send either a session cookie or an API token, not both")
				return
			}
			sess, ok := s.tokenSession(w, r)
			if !ok {
				return
			}
			_, pattern := mux.Handler(r)
			if msg := tokenRefused(r, pattern, sess); msg != "" {
				httpError(w, http.StatusForbidden, msg)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxSessionKey{}, sess)))
			return
		}
		// Only the API and the Google redirects use the session: static files and the SPA shell don't
		// need a database lookup.
		if api || strings.HasPrefix(r.URL.Path, "/auth/") {
			sess, err := s.loadSession(w, r)
			if err != nil {
				s.Log.Error("session lookup failed", "err", err, "req", requestID(r))
				httpError(w, http.StatusServiceUnavailable, "temporarily unavailable, try again")
				return
			}
			if sess != nil {
				r = r.WithContext(context.WithValue(r.Context(), ctxSessionKey{}, sess))
			}
		}
		if api && !publicAPI[r.URL.Path] && sessionFrom(r) == nil {
			httpError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		cop.Handler(next).ServeHTTP(w, r)
	})
}

const (
	apiTokenTouch    = time.Minute // last-used is written at most this often
	apiTokenFailures = 30          // failed token attempts per IP per 15 minutes
)

// tokenSession authenticates a request by its personal API token. It writes the error response
// itself (ok=false): 401 for a missing, unknown, expired or disabled user's token, 429 when the IP
// has failed too often.
func (s *Server) tokenSession(w http.ResponseWriter, r *http.Request) (*store.Session, bool) {
	// Failures are counted per IP, but a valid token always works: 256-bit tokens can't be guessed, and
	// refusing good tokens after someone else's failures (shared NAT, CI runners) would let anyone on
	// the network lock everyone out. Over the limit, bad tokens get 429 instead of 401.
	key := "apitoken-ip:" + s.limitIP(r)
	refuse := func(msg string) (*store.Session, bool) {
		if !s.limits.allow(key, apiTokenFailures, 15*time.Minute, true) {
			httpError(w, http.StatusTooManyRequests, "too many failed API token attempts: wait 15 minutes")
			return nil, false
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="owl"`)
		httpError(w, http.StatusUnauthorized, msg)
		return nil, false
	}
	secret := strings.TrimSpace(r.Header.Get("Authorization")[len("Bearer "):])
	if !auth.IsAPIToken(secret) {
		return refuse("invalid API token: expected Authorization: Bearer owl_pat_…")
	}
	t, u, err := s.Store.APITokenByHash(r.Context(), auth.Hash(secret))
	if errors.Is(err, store.ErrNotFound) {
		return refuse("invalid or revoked API token: create a new one in Owl under Account → API tokens")
	} else if err != nil {
		s.Log.Error("API token lookup failed", "err", err, "req", requestID(r))
		httpError(w, http.StatusServiceUnavailable, "temporarily unavailable, try again")
		return nil, false
	}
	now := time.Now()
	if t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) {
		return refuse("this API token expired: create a new one in Owl under Account → API tokens")
	}
	if !u.Active() {
		return refuse("this API token's user is disabled")
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > apiTokenTouch {
		if err := s.Store.TouchAPIToken(r.Context(), t.ID); err != nil {
			s.Log.Warn("API token touch failed", "err", err)
		}
	}
	return &store.Session{User: *u, APIToken: t}, true
}

// readOnlyPOST are POST endpoints that change nothing (the body is too big or structured for a query),
// so read-only tokens may call them: a CI job checking users-file drift needs no write token.
var readOnlyPOST = map[string]bool{"/api/v1/users/plan": true}

func readOnly(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodPost && readOnlyPOST[r.URL.Path]
}

// tokenWrites are the only changes an API token can make (route patterns, as registered): what the CLI
// does — clusters, a backup, users — and revoking itself. Everything else that changes something is
// browser-only, so a new route is closed to tokens until it's added here on purpose. Sign-in links for
// existing accounts (reset, invite again), passwords, sign-in settings and token management are
// deliberately absent: a leaked token must not be able to take over or create sign-ins.
var tokenWrites = map[string]bool{
	"POST /api/v1/clusters":                     true,
	"POST /api/v1/clusters/{name}/rotate-token": true,
	"DELETE /api/v1/clusters/{name}":            true,
	"POST /api/v1/backups":                      true,
	"POST /api/v1/users":                        true,
	"PATCH /api/v1/users/{id}":                  true,
	"POST /api/v1/users/plan":                   true, // reads (readOnlyPOST)
	"POST /api/v1/users/apply":                  true,
	"DELETE /api/v1/tokens/current":             true,
}

// tokenRefused: reads are allowed except sign-in internals (/auth/… other than GET /auth/me); writes only
// from tokenWrites, and only with an effective write scope (read-only POSTs excepted).
func tokenRefused(r *http.Request, pattern string, sess *store.Session) string {
	if readOnly(r) && r.Method != http.MethodPost {
		if strings.HasPrefix(r.URL.Path, "/api/v1/auth/") && pattern != "GET /api/v1/auth/me" {
			return "API tokens can't do this: use the Owl web UI"
		}
		return ""
	}
	if !tokenWrites[pattern] {
		return "API tokens can't do this: use the Owl web UI"
	}
	if !readOnly(r) && !sess.CanWrite() && pattern != "DELETE /api/v1/tokens/current" {
		return "this API token is read-only"
	}
	return ""
}

// isAPIBearer: the request carries a personal API token. Other Authorization headers (basic auth or an
// SSO proxy's bearer token forwarded by a reverse proxy) are ignored, so they don't break the UI.
func isAPIBearer(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	return len(h) > len(p) && strings.EqualFold(h[:len(p)], p) && strings.HasPrefix(strings.TrimSpace(h[len(p):]), auth.APITokenPrefix)
}

// loadSession returns the valid session of the request's cookie, or nil (clearing a cookie whose
// session is gone). A database error is returned as such and leaves the cookie alone: a hiccup must
// not sign anyone out.
func (s *Server) loadSession(w http.ResponseWriter, r *http.Request) (*store.Session, error) {
	c, err := r.Cookie(cookieName(r, "session"))
	if err != nil || c.Value == "" || len(c.Value) > 100 {
		return nil, nil
	}
	hash := auth.Hash(c.Value)
	sess, err := s.Store.SessionByHash(r.Context(), hash)
	if errors.Is(err, store.ErrNotFound) {
		setCookie(w, r, "session", "", -1)
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	now := time.Now()
	if now.After(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) > sessionIdle || !sess.User.Active() {
		_ = s.Store.DeleteSession(context.WithoutCancel(r.Context()), hash)
		setCookie(w, r, "session", "", -1)
		return nil, nil
	}
	if now.Sub(sess.LastSeenAt) > sessionTouch {
		if err := s.Store.TouchSession(r.Context(), hash); err != nil {
			s.Log.Warn("session touch failed", "err", err)
		}
	}
	return sess, nil
}

// startSession signs the user in on this browser.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User, method string) error {
	secret, hash := auth.NewSecret()
	if err := s.Store.CreateSession(r.Context(), hash, u.ID, time.Now().Add(sessionAbsolute)); err != nil {
		return err
	}
	setCookie(w, r, "session", secret, sessionAbsolute)
	if err := s.Store.RecordLogin(r.Context(), u.ID); err != nil {
		s.Log.Warn("record login failed", "err", err)
	}
	s.Log.Info("signed in", "user", u.Email, "method", method, "remote", s.clientIP(r), "req", requestID(r))
	return nil
}

// requireAdmin guards admin endpoints: a signed-in user with the admin role.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFrom(r)
		if sess == nil {
			httpError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if sess.User.Role != store.RoleAdmin {
			httpError(w, http.StatusForbidden, "admins only: ask an admin to make this change")
			return
		}
		if !readOnly(r) && !sess.CanWrite() {
			httpError(w, http.StatusForbidden, "this API token is read-only") // also checked in authn
			return
		}
		next(w, r)
	}
}

// clientIP is the address the request came from: the TCP peer, or — when the peer is a trusted proxy
// — the right-most X-Forwarded-For entry that isn't a trusted proxy (entries further left are
// client-supplied and can be forged).
func (s *Server) clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !s.trusted(peer) {
		return host
	}
	xff := r.Header.Values("X-Forwarded-For")
	for i := len(xff) - 1; i >= 0; i-- {
		parts := strings.Split(xff[i], ",")
		for j := len(parts) - 1; j >= 0; j-- {
			a, err := netip.ParseAddr(strings.TrimSpace(parts[j]))
			if err != nil {
				return host // malformed: don't guess
			}
			if !s.trusted(a) {
				return a.Unmap().String()
			}
		}
	}
	return host
}

// limitIP is the client address as a rate-limit key: IPv6 is grouped by /64 (one subscriber gets a
// whole /64, so per-address keys would let one attacker mint unlimited buckets and flood the limiter).
func (s *Server) limitIP(r *http.Request) string {
	ip := s.clientIP(r)
	a, err := netip.ParseAddr(ip)
	if err != nil || a.Is4() || a.Is4In6() {
		return ip
	}
	p, _ := a.Prefix(64)
	return p.String()
}

func (s *Server) trusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.Auth.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// --- rate limiting (in memory; one server) ---

type limiter struct {
	mu        sync.Mutex
	m         map[string]*window
	lastPrune time.Time
}

// maxLimiterKeys bounds memory under a flood of distinct keys; past it, a random entry is evicted
// (refusing new keys instead would let a flood lock everyone out). Evicting one particular live
// counter this way takes ~maxLimiterKeys counted requests — far more than the attempts it limits —
// and per-IP keys group IPv6 by /64 (limitIP), so floods need many real networks.
const maxLimiterKeys = 100000

type window struct {
	until time.Time
	n     int
}

// allow reports whether key has had fewer than max hits in its current window, and counts this one
// when count is true (check and count are one step under the lock, so parallel requests can't all
// pass the check). A check-only call never creates an entry.
func (l *limiter) allow(key string, max int, per time.Duration, count bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.m == nil {
		l.m = map[string]*window{}
	}
	if now.Sub(l.lastPrune) > time.Minute { // expired windows go at most once a minute
		l.lastPrune = now
		for k, w := range l.m {
			if now.After(w.until) {
				delete(l.m, k)
			}
		}
	}
	w := l.m[key]
	if w == nil || now.After(w.until) {
		if !count {
			return true
		}
		if w == nil && len(l.m) >= maxLimiterKeys {
			for k := range l.m { // map order is random: evict one arbitrary entry
				delete(l.m, k)
				break
			}
		}
		w = &window{until: now.Add(per)}
		l.m[key] = w
	}
	if w.n >= max {
		return false
	}
	if count {
		w.n++
	}
	return true
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}

// undo takes back one hit (an attempt counted up front that turned out fine).
func (l *limiter) undo(key string) {
	l.mu.Lock()
	if w := l.m[key]; w != nil && w.n > 0 {
		w.n--
	}
	l.mu.Unlock()
}
