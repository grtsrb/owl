package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"owl/internal/store"
)

func userID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathID(w, r, "user not found")
}

// pathID reads a positive {id}; anything else is a 404 with notFound.
func pathID(w http.ResponseWriter, r *http.Request, notFound string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpError(w, http.StatusNotFound, notFound)
		return 0, false
	}
	return id, true
}

func (s *Server) userError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrLastAdmin):
		httpError(w, http.StatusConflict, "Owl needs at least one active admin: make someone else an admin first.")
	case errors.Is(err, store.ErrNotFound):
		httpError(w, http.StatusNotFound, "user not found")
	case errors.Is(err, store.ErrExists):
		httpError(w, http.StatusConflict, "a user with that email already exists")
	default:
		s.storeError(w, err)
	}
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, us)
}

// linkJSON is the answer to anything that creates a one-time link: the link itself (shown to the
// admin once, to copy) and whether it was emailed.
type linkJSON struct {
	User       *store.User `json:"user"`
	Link       string      `json:"link"`
	Emailed    bool        `json:"emailed"`
	EmailError string      `json:"emailError,omitempty"`
}

// sendLink emails a link when email is configured; a failed email isn't a failed request (the admin
// still has the link to copy).
func (s *Server) sendLink(r *http.Request, u *store.User, kind store.AuthTokenKind) (linkJSON, error) {
	link, err := s.newLink(r.Context(), u, kind)
	if err != nil {
		return linkJSON{}, err
	}
	out := linkJSON{User: u, Link: link}
	if s.Auth.Mail != nil && s.Auth.PublicURL != nil {
		by := sessionFrom(r).User.Email
		msg := inviteMail(u.Email, link, by)
		if kind == store.TokenReset {
			msg = resetMail(u.Email, link, true)
		}
		if err := s.Auth.Mail.Send(r.Context(), msg); err != nil {
			s.Log.Error("email failed", "to", u.Email, "kind", kind, "err", err, "req", requestID(r))
			out.EmailError = "The email couldn't be sent (see the server log): copy the link instead."
		} else {
			out.Emailed = true
		}
	}
	if sessionFrom(r).APIToken != nil {
		// Never to a token: with it, a leaked token could create a sign-in that outlives it. Without
		// email, an admin sends the invite from Settings → Users (Send invite again).
		out.Link = ""
	}
	return out, nil
}

func (s *Server) inviteUser(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email string     `json:"email"`
		Role  store.Role `json:"role"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	email, ok := ValidEmail(b.Email)
	if !ok {
		httpError(w, http.StatusBadRequest, "enter a plain email address, like name@example.com")
		return
	}
	if !b.Role.Valid() {
		httpError(w, http.StatusBadRequest, "role must be admin or viewer")
		return
	}
	u, err := s.Store.CreateUser(r.Context(), email, b.Role)
	if err != nil {
		s.userError(w, err)
		return
	}
	out, err := s.sendLink(r, u, store.TokenInvite)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "user invited", "user", u.Email, "role", u.Role, "emailed", out.Emailed)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) reinviteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	u, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.userError(w, err)
		return
	}
	if u.ActivatedAt != nil || u.DisabledAt != nil {
		httpError(w, http.StatusConflict, "only pending invites can be sent again")
		return
	}
	out, err := s.sendLink(r, u, store.TokenInvite)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "invite sent again", "user", u.Email, "emailed", out.Emailed)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) resetUser(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	if !s.requirePasswordLogin(w, r, http.StatusConflict, "Password sign-in is off. In an emergency, run `owl db user reset-link` on the server.") {
		return
	}
	u, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.userError(w, err)
		return
	}
	if !u.Active() {
		httpError(w, http.StatusConflict, "only active users can get a reset link (send a pending invite again instead)")
		return
	}
	out, err := s.sendLink(r, u, store.TokenReset)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "password reset link created", "user", u.Email, "emailed", out.Emailed)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	var b struct {
		Role     store.Role `json:"role"`
		Disabled bool       `json:"disabled"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if !b.Role.Valid() {
		httpError(w, http.StatusBadRequest, "role must be admin or viewer")
		return
	}
	if id == sessionFrom(r).User.ID {
		httpError(w, http.StatusConflict, "you can't change your own role or disable yourself: ask another admin")
		return
	}
	before, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.userError(w, err)
		return
	}
	if b.Role != store.RoleAdmin || b.Disabled {
		if !s.checkGoogleAdmin(w, r, id) {
			return
		}
	}
	u, err := s.Store.UpdateUser(r.Context(), id, b.Role, b.Disabled)
	if err != nil {
		s.userError(w, err)
		return
	}
	if before.Role != u.Role {
		s.audit(r, "user role changed", "user", u.Email, "from", before.Role, "to", u.Role)
	}
	if (before.DisabledAt != nil) != (u.DisabledAt != nil) {
		action := "user enabled"
		if u.DisabledAt != nil {
			action = "user disabled"
		}
		s.audit(r, action, "user", u.Email)
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := userID(w, r)
	if !ok {
		return
	}
	if id == sessionFrom(r).User.ID {
		httpError(w, http.StatusConflict, "you can't delete yourself: ask another admin")
		return
	}
	u, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.userError(w, err)
		return
	}
	if !s.checkGoogleAdmin(w, r, id) {
		return
	}
	if err := s.Store.DeleteUser(r.Context(), id); err != nil {
		s.userError(w, err)
		return
	}
	s.audit(r, "user deleted", "user", u.Email)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// checkGoogleAdmin refuses (409) to demote, disable or delete user id when password sign-in is off
// and id is the last active admin who can sign in with Google — nobody could manage Owl afterwards.
func (s *Server) checkGoogleAdmin(w http.ResponseWriter, r *http.Request, id int64) bool {
	err := GoogleAdminGuard(r.Context(), s.Store, s.Auth.Google != nil, id)
	if errors.Is(err, store.ErrLastAdmin) {
		httpError(w, http.StatusConflict, "Password sign-in is off and this is the last admin who signs in with Google: make another Google-linked user an admin first (or turn password sign-in back on).")
		return false
	} else if err != nil {
		s.fail(w, err)
		return false
	}
	return true
}

// GoogleAdminGuard returns ErrLastAdmin when password sign-in is off (and Google configured) and user
// id is the only active admin with a linked Google account. Also used by `owl db user delete`.
func GoogleAdminGuard(ctx context.Context, st store.Store, googleConfigured bool, id int64) error {
	return googleAdminGuard(ctx, st, googleConfigured, map[int64]plannedUser{id: {Role: store.RoleViewer, Disabled: true}})
}

// plannedUser is a user's role and disabled state after a change.
type plannedUser struct {
	Role     store.Role
	Disabled bool
}

// googleAdminGuard: with password sign-in off (and Google configured), at least one active admin who
// signs in with Google must remain after all the planned changes together (checked as a whole, so a
// users file can't demote every one of them while each change alone looks fine).
func googleAdminGuard(ctx context.Context, st store.Store, googleConfigured bool, after map[int64]plannedUser) error {
	m, err := st.GetSettings(ctx)
	if err != nil {
		return err
	}
	if m[settingPasswordLogin] != "off" || !googleConfigured {
		return nil
	}
	us, err := st.ListUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range us {
		role, disabled := u.Role, u.DisabledAt != nil
		if p, ok := after[u.ID]; ok {
			role, disabled = p.Role, p.Disabled
		}
		if role == store.RoleAdmin && !disabled && u.ActivatedAt != nil && u.HasGoogle {
			return nil
		}
	}
	return store.ErrLastAdmin
}

type authSettingsJSON struct {
	PasswordLogin bool `json:"passwordLogin"`
	Google        struct {
		Configured  bool   `json:"configured"`
		Domain      string `json:"domain,omitempty"`
		RedirectURL string `json:"redirectURL,omitempty"`
		// LinkedAdmins: active admins who have signed in with Google (password sign-in can only be
		// turned off when there's at least one).
		LinkedAdmins int `json:"linkedAdmins"`
	} `json:"google"`
	Mail      any    `json:"mail"`
	PublicURL string `json:"publicURL,omitempty"`
}

func (s *Server) loadAuthSettings(r *http.Request) (authSettingsJSON, error) {
	var out authSettingsJSON
	pw, err := s.passwordLogin(r.Context())
	if err != nil {
		return out, err
	}
	us, err := s.Store.ListUsers(r.Context())
	if err != nil {
		return out, err
	}
	out.PasswordLogin = pw
	if g := s.Auth.Google; g != nil {
		out.Google.Configured, out.Google.Domain, out.Google.RedirectURL = true, g.Domain, g.RedirectURL
	}
	for i := range us {
		if us[i].Role == store.RoleAdmin && us[i].Active() && us[i].HasGoogle {
			out.Google.LinkedAdmins++
		}
	}
	out.Mail = s.Auth.MailInfo
	if s.Auth.PublicURL != nil {
		out.PublicURL = s.Auth.PublicURL.String()
	}
	return out, nil
}

func (s *Server) getAuthSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.loadAuthSettings(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) putAuthSettings(w http.ResponseWriter, r *http.Request) {
	var b struct {
		PasswordLogin bool `json:"passwordLogin"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	cur, err := s.loadAuthSettings(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !b.PasswordLogin && (!cur.Google.Configured || cur.Google.LinkedAdmins == 0) {
		httpError(w, http.StatusConflict, "Password sign-in can only be turned off once Google sign-in is set up and at least one admin has signed in with Google — otherwise nobody could sign in.")
		return
	}
	v := "on"
	if !b.PasswordLogin {
		v = "off"
	}
	if err := s.Store.SetSetting(r.Context(), settingPasswordLogin, v); err != nil {
		s.fail(w, err)
		return
	}
	if cur.PasswordLogin != b.PasswordLogin {
		s.audit(r, "password sign-in turned "+v)
	}
	cur.PasswordLogin = b.PasswordLogin
	writeJSON(w, http.StatusOK, cur)
}

func (s *Server) testMail(w http.ResponseWriter, r *http.Request) {
	if s.Auth.Mail == nil {
		httpError(w, http.StatusConflict, "Email isn't configured: set OWL_SMTP_HOST and the other OWL_SMTP_* variables on the server.")
		return
	}
	if !s.limits.allow("mailtest", 10, time.Hour, true) {
		httpError(w, http.StatusTooManyRequests, "Too many test emails. Try again in an hour.")
		return
	}
	to := sessionFrom(r).User.Email
	if err := s.Auth.Mail.Send(r.Context(), testMailMessage(to)); err != nil {
		s.Log.Error("test email failed", "to", to, "err", err, "req", requestID(r))
		httpError(w, http.StatusBadGateway, "Sending failed: "+err.Error())
		return
	}
	s.audit(r, "test email sent", "to", to)
	writeJSON(w, http.StatusOK, map[string]string{"to": to})
}
