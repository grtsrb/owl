package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"owl/internal/auth"
	mailer "owl/internal/mail"
	"owl/internal/store"
)

func (s *Server) signinRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/auth/config", s.authConfig)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/auth/me", s.me)
	mux.HandleFunc("POST /api/v1/auth/password", s.changePassword)
	mux.HandleFunc("POST /api/v1/auth/token", s.tokenInfo)
	mux.HandleFunc("POST /api/v1/auth/accept", s.acceptToken)
	mux.HandleFunc("POST /api/v1/auth/forgot", s.forgotPassword)
	mux.HandleFunc("GET /auth/google/start", s.googleStart)
	mux.HandleFunc("GET /auth/google/callback", s.googleCallback)
	// Admin: users, sign-in settings, test email.
	mux.HandleFunc("GET /api/v1/users", s.requireAdmin(s.listUsers))
	mux.HandleFunc("POST /api/v1/users", s.requireAdmin(s.inviteUser))
	mux.HandleFunc("PATCH /api/v1/users/{id}", s.requireAdmin(s.updateUser))
	mux.HandleFunc("DELETE /api/v1/users/{id}", s.requireAdmin(s.deleteUser))
	mux.HandleFunc("POST /api/v1/users/plan", s.requireAdmin(s.usersPlan))
	mux.HandleFunc("POST /api/v1/users/apply", s.requireAdmin(s.usersApply))
	mux.HandleFunc("POST /api/v1/users/{id}/invite", s.requireAdmin(s.reinviteUser))
	mux.HandleFunc("POST /api/v1/users/{id}/reset", s.requireAdmin(s.resetUser))
	mux.HandleFunc("GET /api/v1/auth/settings", s.requireAdmin(s.getAuthSettings))
	mux.HandleFunc("PUT /api/v1/auth/settings", s.requireAdmin(s.putAuthSettings))
	mux.HandleFunc("POST /api/v1/mail/test", s.requireAdmin(s.testMail))
}

// passwordLogin reports whether password sign-in is on. "off" only counts while Google sign-in is
// configured: if the Google settings disappear (env changed, restart), passwords come back on by
// themselves instead of leaving nobody able to sign in.
func (s *Server) passwordLogin(ctx context.Context) (bool, error) {
	m, err := s.Store.GetSettings(ctx)
	if err != nil {
		return false, err
	}
	return m[settingPasswordLogin] != "off" || s.Auth.Google == nil, nil
}

type authConfigJSON struct {
	Password bool `json:"password"` // password sign-in on
	Google   bool `json:"google"`   // Google sign-in configured
	Forgot   bool `json:"forgot"`   // "Forgot password?" available (password sign-in + email)
}

func (s *Server) authConfig(w http.ResponseWriter, r *http.Request) {
	pw, err := s.passwordLogin(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, authConfigJSON{Password: pw, Google: s.Auth.Google != nil, Forgot: pw && s.Auth.Mail != nil})
}

type meJSON struct {
	Email       string     `json:"email"`
	Role        store.Role `json:"role"`
	HasPassword bool       `json:"hasPassword"`
	HasGoogle   bool       `json:"hasGoogle"`
	DigestEmail bool       `json:"digestEmail"`
	// EmailAvailable: Owl can send email (only on /auth/me, for the Account page's digest switch).
	EmailAvailable bool `json:"emailAvailable,omitempty"`
	// Token: the request used a personal API token (the CLI's `owl login` / `owl whoami`). Scope is the
	// effective one: "read" for a write token whose owner is no longer an admin.
	Token *meTokenJSON `json:"token,omitempty"`
}

type meTokenJSON struct {
	Name      string           `json:"name"`
	Scope     store.TokenScope `json:"scope"`
	ExpiresAt *time.Time       `json:"expiresAt,omitempty"`
}

func toMe(u *store.User) meJSON {
	return meJSON{Email: u.Email, Role: u.Role, HasPassword: u.HasPassword, HasGoogle: u.HasGoogle, DigestEmail: u.DigestEmail}
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	me := toMe(&sess.User)
	me.EmailAvailable = s.Auth.Mail != nil
	if t := sess.APIToken; t != nil {
		scope := store.ScopeRead
		if sess.CanWrite() {
			scope = store.ScopeWrite
		}
		me.Token = &meTokenJSON{Name: t.Name, Scope: scope, ExpiresAt: t.ExpiresAt}
	}
	writeJSON(w, http.StatusOK, me)
}

// ValidEmail: a bare address (no display name), lowercase, at most 254 characters.
func ValidEmail(raw string) (string, bool) {
	e := strings.ToLower(strings.TrimSpace(raw))
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || len(e) > 254 || !strings.Contains(e[strings.IndexByte(e, '@')+1:], ".") && !strings.HasSuffix(e, "@localhost") {
		return "", false
	}
	return e, true
}

const wrongLogin = "Wrong email or password."

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if on, err := s.passwordLogin(r.Context()); err != nil {
		s.fail(w, err)
		return
	} else if !on {
		httpError(w, http.StatusForbidden, "Password sign-in is turned off: sign in with Google.")
		return
	}
	email := strings.ToLower(strings.TrimSpace(b.Email))
	ip := s.limitIP(r)
	// Failures are counted per IP (one source trying many accounts) and per account from that IP — not
	// per account alone, which would let anyone lock a user out by failing on purpose. Guessing from
	// many IPs is still bounded by argon2id's cost and the per-IP limit. A success clears the count.
	// Attempts are counted before the (slow) password check, so parallel requests can't all slip past
	// the limit; a successful sign-in takes its count back.
	userKey := "login-user:" + email + "|" + ip
	ipOK := s.limits.allow("login-ip:"+ip, 30, 15*time.Minute, true)
	if !ipOK || !s.limits.allow(userKey, 10, 15*time.Minute, true) {
		if ipOK {
			s.limits.undo("login-ip:" + ip) // refused for the account: don't also spend the IP's budget
		}
		httpError(w, http.StatusTooManyRequests, "Too many failed sign-ins. Wait 15 minutes and try again.")
		return
	}
	u, err := s.Store.UserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	hash := ""
	if u != nil {
		hash = u.PasswordHash
	}
	ok, err := auth.VerifyPassword(r.Context(), hash, b.Password) // runs even for unknown emails (timing)
	if err != nil {
		if r.Context().Err() != nil {
			return // client went away while waiting for a hashing slot
		}
		s.Log.Error("password check failed (corrupt hash?)", "user", email, "err", err)
		ok = false
	}
	if !ok || u == nil || !u.Active() {
		s.Log.Warn("sign-in failed", "user", email, "remote", s.clientIP(r), "req", requestID(r))
		httpError(w, http.StatusUnauthorized, wrongLogin)
		return
	}
	s.limits.reset(userKey)
	s.limits.undo("login-ip:" + ip)
	if err := s.startSession(w, r, u, "password"); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toMe(u))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName(r, "session")); err == nil && c.Value != "" {
		if err := s.Store.DeleteSession(r.Context(), auth.Hash(c.Value)); err != nil {
			s.fail(w, err)
			return
		}
	}
	setCookie(w, r, "session", "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	sess := sessionFrom(r)
	if on, err := s.passwordLogin(r.Context()); err != nil {
		s.fail(w, err)
		return
	} else if !on {
		httpError(w, http.StatusForbidden, "Password sign-in is turned off.")
		return
	}
	if !sess.User.HasPassword {
		httpError(w, http.StatusConflict, "You sign in with Google and have no password. Use “Forgot password?” on the sign-in page to set one.")
		return
	}
	key := "password-user:" + sess.User.Email
	if !s.limits.allow(key, 10, 15*time.Minute, true) { // counted up front, taken back on success
		httpError(w, http.StatusTooManyRequests, "Too many attempts. Wait 15 minutes and try again.")
		return
	}
	if err := auth.CheckPasswordRule(b.New); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	ok, err := auth.VerifyPassword(r.Context(), sess.User.PasswordHash, b.Current)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		httpError(w, http.StatusForbidden, "The current password is wrong.")
		return
	}
	hash, err := auth.HashPassword(r.Context(), b.New)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.limits.reset(key)
	if err := s.Store.SetPassword(r.Context(), sess.User.ID, hash, sess.TokenHash); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "password changed", "user", sess.User.Email)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// tokenInfo tells the invite/reset page whose link it is, without using it up.
func (s *Server) tokenInfo(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if !s.limits.allow("token-ip:"+s.limitIP(r), 60, 15*time.Minute, true) {
		httpError(w, http.StatusTooManyRequests, "Too many attempts. Wait 15 minutes and try again.")
		return
	}
	u, kind, err := s.Store.PeekAuthToken(r.Context(), auth.Hash(strings.TrimSpace(b.Token)))
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "This link is invalid, was already used or has expired. Ask an admin for a new one.")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	pw, err := s.passwordLogin(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"email": u.Email, "kind": kind,
		"password": pw || kind == store.TokenReset, "google": s.Auth.Google != nil})
}

// acceptToken uses an invite or reset link: sets the password and signs the user in. A reset link
// works even with password sign-in off — it's the emergency way in (`owl db user reset-link`).
func (s *Server) acceptToken(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if !s.limits.allow("token-ip:"+s.limitIP(r), 60, 15*time.Minute, true) {
		httpError(w, http.StatusTooManyRequests, "Too many attempts. Wait 15 minutes and try again.")
		return
	}
	if err := auth.CheckPasswordRule(b.Password); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash := auth.Hash(strings.TrimSpace(b.Token))
	_, kind, err := s.Store.PeekAuthToken(r.Context(), hash)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "This link is invalid, was already used or has expired. Ask an admin for a new one.")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	if kind == store.TokenInvite {
		if on, err := s.passwordLogin(r.Context()); err != nil {
			s.fail(w, err)
			return
		} else if !on {
			httpError(w, http.StatusForbidden, "Password sign-in is turned off: accept the invite by signing in with Google.")
			return
		}
	}
	pwHash, err := auth.HashPassword(r.Context(), b.Password)
	if err != nil {
		s.fail(w, err)
		return
	}
	u, kind, err := s.Store.ConsumeAuthToken(r.Context(), hash) // atomic: a link works once
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "This link was just used or has expired.")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.SetPassword(r.Context(), u.ID, pwHash, ""); err != nil {
		s.fail(w, err)
		return
	}
	if u, err = s.Store.GetUser(r.Context(), u.ID); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.startSession(w, r, u, string(kind)+" link"); err != nil {
		s.fail(w, err)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), ctxSessionKey{}, &store.Session{User: *u}))
	if kind == store.TokenInvite {
		s.audit(r, "invite accepted", "user", u.Email)
	} else {
		s.audit(r, "password reset", "user", u.Email)
	}
	writeJSON(w, http.StatusOK, toMe(u))
}

// forgotPassword always answers the same way and sends the email in the background, so neither the
// reply nor its timing tells whether the account exists.
func (s *Server) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	pw, err := s.passwordLogin(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if !pw || s.Auth.Mail == nil || s.Auth.PublicURL == nil {
		httpError(w, http.StatusNotFound, "Password reset by email isn't available. Ask an admin for a reset link.")
		return
	}
	ip := s.limitIP(r)
	if !s.limits.allow("forgot-ip:"+ip, 10, time.Hour, true) {
		httpError(w, http.StatusTooManyRequests, "Too many requests. Try again in an hour.")
		return
	}
	const answer = "If that account exists, we sent it a link to set a new password. It works for an hour."
	email, ok := ValidEmail(b.Email)
	if !ok || !s.limits.allow("forgot-user:"+email, 3, time.Hour, true) {
		writeJSON(w, http.StatusOK, map[string]string{"message": answer})
		return
	}
	req, remote := requestID(r), s.clientIP(r)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		u, err := s.Store.UserByEmail(ctx, email)
		if err != nil || !u.Active() {
			return // unknown, invited (use the invite) or disabled: nothing is sent
		}
		link, err := s.newLink(ctx, u, store.TokenReset)
		if err == nil {
			err = s.Auth.Mail.Send(ctx, resetMail(u.Email, link, false))
		}
		if err != nil {
			s.Log.Error("password reset email failed", "user", u.Email, "err", err, "req", req)
			return
		}
		s.Log.Info("password reset email sent", "user", u.Email, "remote", remote, "req", req)
	}()
	writeJSON(w, http.StatusOK, map[string]string{"message": answer})
}

// newLink creates a one-time link for u and returns it — absolute when the public URL is known,
// else a path the UI prefixes with its own origin.
func (s *Server) newLink(ctx context.Context, u *store.User, kind store.AuthTokenKind) (string, error) {
	secret, hash := auth.NewSecret()
	ttl := InviteTTL
	if kind == store.TokenReset {
		ttl = ResetTTL
	}
	if err := s.Store.CreateAuthToken(ctx, hash, u.ID, kind, time.Now().Add(ttl)); err != nil {
		return "", err
	}
	return LinkPath(s.Auth.PublicURL, kind, secret), nil
}

// LinkPath builds an invite/reset link. The secret is in the fragment (#), which browsers never send
// to the server or in a Referer — it stays out of access logs and proxies.
func LinkPath(base *url.URL, kind store.AuthTokenKind, secret string) string {
	p := "/invite#" + secret
	if kind == store.TokenReset {
		p = "/reset#" + secret
	}
	if base == nil {
		return p
	}
	return strings.TrimRight(base.String(), "/") + p
}

func inviteMail(to, link, by string) mailer.Message {
	return mailer.Message{To: to, Subject: "You're invited to Owl", Body: fmt.Sprintf(`Hi,

%s invited you to Owl, which tracks the third-party components running in our Kubernetes clusters.

Open this link to set your password and sign in (it works once, for 72 hours):

%s

If you didn't expect this, ignore this email.
`, by, link)}
}

func resetMail(to, link string, byAdmin bool) mailer.Message {
	why := "Someone (hopefully you) asked to reset your Owl password."
	if byAdmin {
		why = "An Owl admin sent you a link to set a new password."
	}
	return mailer.Message{To: to, Subject: "Set a new Owl password", Body: fmt.Sprintf(`Hi,

%s Open this link to choose a new one (it works once, for an hour):

%s

If you didn't ask for this, ignore this email: your password stays as it is.
`, why, link)}
}

func testMailMessage(to string) mailer.Message {
	return mailer.Message{To: to, Subject: "Owl test email", Body: `Hi,

This is a test email from Owl. If you can read it, email is set up correctly.
`}
}
