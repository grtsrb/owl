package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"owl/internal/auth"
	mailer "owl/internal/mail"
	"owl/internal/store"
)

// Personal API tokens (milestone 12): created in the UI (Account → API tokens) with a browser session,
// used by the CLI as a bearer token (authn.tokenSession).

const (
	maxTokenName     = 40
	maxTokensPerUser = 50
	tokenWarnBefore  = 7 * 24 * time.Hour
	defaultTokenDays = 90
	tokenNameAllowed = "letters, digits, spaces and . _ -"
)

// tokenExpiryDays are the choices offered at creation; 0 = never expires.
var tokenExpiryDays = map[int]bool{30: true, 90: true, 365: true, 0: true}

func (s *Server) tokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tokens", s.listOwnTokens)
	mux.HandleFunc("POST /api/v1/tokens", s.createToken)
	mux.HandleFunc("DELETE /api/v1/tokens/current", s.revokeCurrentToken)
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.revokeToken)
	mux.HandleFunc("GET /api/v1/tokens/all", s.requireAdmin(s.listAllTokens))
}

func validTokenName(n string) bool {
	if n == "" || len(n) > maxTokenName || n[0] == ' ' || n[len(n)-1] == ' ' {
		return false
	}
	for _, c := range n {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ' ' || c == '.' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

func (s *Server) listOwnTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Store.ListAPITokens(r.Context(), sessionFrom(r).User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) listAllTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Store.ListAPITokens(r.Context(), 0)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

type createdTokenJSON struct {
	Token  *store.APIToken `json:"token"`
	Secret string          `json:"secret"` // shown once
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if sess.APIToken != nil { // authn already refuses this; belt and braces
		httpError(w, http.StatusForbidden, "API tokens can't create tokens")
		return
	}
	var b struct {
		Name        string           `json:"name"`
		Scope       store.TokenScope `json:"scope"`
		ExpiresDays *int             `json:"expiresDays"` // 30, 90, 365 or 0 (never); missing = 90
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if !validTokenName(b.Name) {
		httpError(w, http.StatusBadRequest, fmt.Sprintf("token name: 1–%d characters, %s", maxTokenName, tokenNameAllowed))
		return
	}
	if b.Scope == "" {
		b.Scope = store.ScopeRead
	}
	if !b.Scope.Valid() {
		httpError(w, http.StatusBadRequest, "scope must be read or write")
		return
	}
	if b.Scope == store.ScopeWrite && sess.User.Role != store.RoleAdmin {
		httpError(w, http.StatusForbidden, "only admins can create tokens that change things")
		return
	}
	days := defaultTokenDays
	if b.ExpiresDays != nil {
		days = *b.ExpiresDays
	}
	if !tokenExpiryDays[days] {
		httpError(w, http.StatusBadRequest, "expiry must be 30, 90 or 365 days, or 0 for never")
		return
	}
	existing, err := s.Store.ListAPITokens(r.Context(), sess.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	live := 0
	for _, t := range existing {
		if t.ExpiresAt == nil || t.ExpiresAt.After(time.Now()) {
			live++
		}
	}
	if live >= maxTokensPerUser {
		httpError(w, http.StatusConflict, fmt.Sprintf("you already have %d tokens: revoke some first", maxTokensPerUser))
		return
	}
	secret, hash, prefix := auth.NewAPIToken()
	t := store.APIToken{UserID: sess.User.ID, Name: b.Name, Prefix: prefix, Scope: b.Scope}
	if days > 0 {
		exp := time.Now().Add(time.Duration(days) * 24 * time.Hour)
		t.ExpiresAt = &exp
	}
	created, err := s.Store.CreateAPIToken(r.Context(), t, hash)
	if errors.Is(err, store.ErrExists) {
		httpError(w, http.StatusConflict, "you already have a token with that name")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "api token created", "name", created.Name, "scope", created.Scope, "expiresDays", days)
	writeJSON(w, http.StatusCreated, createdTokenJSON{Token: created, Secret: secret})
}

// revokeToken: your own token (browser session), or anyone's if you're an admin.
func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpError(w, http.StatusNotFound, "token not found")
		return
	}
	sess := sessionFrom(r)
	owner := sess.User.ID
	if sess.User.Role == store.RoleAdmin {
		owner = 0
	}
	t, err := s.Store.DeleteAPIToken(r.Context(), id, owner)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "token not found")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "api token revoked", "name", t.Name, "owner", t.UserEmail)
	w.WriteHeader(http.StatusNoContent)
}

// revokeCurrentToken is `owl logout`: a token revoking itself (the one thing it may do to tokens).
func (s *Server) revokeCurrentToken(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if sess.APIToken == nil {
		httpError(w, http.StatusBadRequest, "only an API token can revoke itself")
		return
	}
	_, err := s.Store.DeleteAPIToken(r.Context(), sess.APIToken.ID, sess.User.ID)
	switch {
	case errors.Is(err, store.ErrNotFound): // revoked meanwhile (a parallel logout, an admin): nothing to audit
	case err != nil:
		s.fail(w, err)
		return
	default:
		s.audit(r, "api token revoked", "name", sess.APIToken.Name, "owner", sess.User.Email)
	}
	w.WriteHeader(http.StatusNoContent)
}

// warnExpiringTokens emails owners about tokens that expire within a week (once per token). Without
// email it does nothing (and marks nothing, so configuring email later still warns).
func (s *Server) warnExpiringTokens(ctx context.Context) {
	if s.Auth.Mail == nil {
		return
	}
	ts, err := s.Store.ExpiringAPITokens(ctx, time.Now().Add(tokenWarnBefore))
	if err != nil {
		s.Log.Error("expiring API tokens", "err", err)
		return
	}
	for _, t := range ts {
		if err := s.Auth.Mail.Send(ctx, tokenExpiryMail(t, s.publicLink("/account"))); err != nil {
			s.Log.Error("token expiry email failed", "to", t.UserEmail, "err", err)
			continue
		}
		if err := s.Store.MarkAPITokenWarned(ctx, t.ID); err != nil {
			s.Log.Error("mark token warned", "err", err)
		}
	}
}

func tokenExpiryMail(t store.APIToken, link string) mailer.Message {
	where := "in Owl under Account → API tokens"
	if link != "" {
		where = "in Owl under Account → API tokens:\n\n" + link
	}
	return mailer.Message{To: t.UserEmail, Subject: "Your Owl API token \"" + t.Name + "\" expires soon", Body: fmt.Sprintf(`Hi,

Your Owl API token "%s" (%s…) expires on %s. After that, the CLI or CI job using it stops working.

To keep it working, create a new token %s

and give it to the CLI with "owl login", or replace OWL_TOKEN in your CI variables.
`, t.Name, t.Prefix, t.ExpiresAt.UTC().Format("2 January 2006 15:04 UTC"), where)}
}

// publicLink is an absolute link to a UI page, or "" without OWL_PUBLIC_URL.
func (s *Server) publicLink(path string) string {
	if s.Auth.PublicURL == nil {
		return ""
	}
	return strings.TrimRight(s.Auth.PublicURL.String(), "/") + path
}
