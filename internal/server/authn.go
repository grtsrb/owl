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
func (s *Server) authn(next http.Handler) http.Handler {
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
// (refusing new keys instead would let a flood lock everyone out).
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
