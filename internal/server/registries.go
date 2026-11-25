package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"owl/internal/scan"
	"owl/internal/secretbox"
	"owl/internal/store"
)

// Registry credentials in the UI (milestone 15.1, DESIGN.md "Registry credentials in the UI"): the prefix →
// login list Trivy pulls with, write-only (a password is never returned), sealed with OWL_SECRET_KEY.
// Entries from OWL_REGISTRY_AUTH_FILE are listed read-only and win over a UI row with the same prefix.

// RegistryCreds is the server's part: the key and the file's entries (set at start).
type RegistryCreds struct {
	Box  *secretbox.Box   // nil = OWL_SECRET_KEY not set: UI credentials can't be added or used
	File scan.Credentials // OWL_REGISTRY_AUTH_FILE

	mu    sync.Mutex
	tests map[string]scan.LoginResult // last Test per prefix (memory only)
	load  sync.Mutex                  // one reload at a time, so the newest list wins
}

func (s *Server) registryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/registries", s.requireAdmin(s.listRegistries))
	mux.HandleFunc("POST /api/v1/registries", s.requireAdmin(s.addRegistry))
	mux.HandleFunc("PUT /api/v1/registries", s.requireAdmin(s.updateRegistry))
	mux.HandleFunc("DELETE /api/v1/registries", s.requireAdmin(s.deleteRegistry))
	mux.HandleFunc("POST /api/v1/registries/test", s.requireAdmin(s.testRegistry))
}

func credAD(prefix string) []byte { return []byte("owl registry credential\x00" + prefix) }

// registryRow is one line of Settings → Registries. Never carries a password.
type registryRow struct {
	Prefix    string            `json:"prefix"`
	Username  string            `json:"username"`
	Source    string            `json:"source"` // "ui" | "file"
	ExpiresAt *time.Time        `json:"expiresAt,omitempty"`
	UpdatedAt *time.Time        `json:"updatedAt,omitempty"`
	UpdatedBy string            `json:"updatedBy,omitempty"`
	Problem   string            `json:"problem,omitempty"` // why a UI row isn't used
	AuthFail  *time.Time        `json:"authFailedAt,omitempty"`
	LastTest  *scan.LoginResult `json:"lastTest,omitempty"`
}

// credentials merges the file's entries and the UI's (decrypted). rows describe both for the UI.
func (s *Server) credentials(ctx context.Context) (scan.Credentials, []registryRow, error) {
	rc := s.Registries
	if rc == nil {
		rc = &RegistryCreds{}
	}
	stored, err := s.Store.ListRegistryCredentials(ctx)
	if err != nil {
		return nil, nil, err
	}
	creds := slices.Clone(rc.File)
	rows := []registryRow{}
	for _, c := range rc.File {
		rows = append(rows, registryRow{Prefix: c.Prefix, Username: c.Username, Source: "file", ExpiresAt: c.ExpiresAt})
	}
	for _, c := range stored {
		row := registryRow{Prefix: c.Prefix, Username: c.Username, Source: "ui", ExpiresAt: c.ExpiresAt,
			UpdatedAt: &c.UpdatedAt, UpdatedBy: c.UpdatedBy}
		switch {
		case slices.ContainsFunc(rc.File, func(f scan.Credential) bool { return f.Prefix == c.Prefix }):
			row.Problem = "not used: OWL_REGISTRY_AUTH_FILE has the same prefix (the file wins)"
		case rc.Box == nil:
			row.Problem = "not used: OWL_SECRET_KEY isn't set on this server"
		default:
			pw, err := rc.Box.Open(c.Secret, credAD(c.Prefix))
			if err != nil {
				row.Problem = "can't decrypt (saved with another OWL_SECRET_KEY) — replace the password"
				break
			}
			creds = append(creds, scan.Credential{Prefix: c.Prefix, Username: c.Username, Password: string(pw), ExpiresAt: c.ExpiresAt})
		}
		rows = append(rows, row)
	}
	slices.SortStableFunc(rows, func(a, b registryRow) int { return strings.Compare(a.Prefix, b.Prefix) })
	return creds, rows, nil
}

// ReloadCredentials hands the merged list to the scanner (at start, after a change or a restore).
func (s *Server) ReloadCredentials(ctx context.Context) error {
	if s.Registries != nil {
		s.Registries.load.Lock()
		defer s.Registries.load.Unlock()
	}
	creds, rows, err := s.credentials(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Problem != "" {
			s.Log.Warn("registry credential not used", "prefix", r.Prefix, "why", r.Problem)
		}
	}
	if s.Scans != nil {
		s.Scans.SetCredentials(ctx, creds)
	}
	return nil
}

func browserOnly(w http.ResponseWriter, r *http.Request) bool {
	if sess := sessionFrom(r); sess != nil && sess.APIToken != nil {
		httpError(w, http.StatusForbidden, "API tokens can't do this: use the Owl web UI")
		return false
	}
	return true
}

func (s *Server) listRegistries(w http.ResponseWriter, r *http.Request) {
	if !browserOnly(w, r) {
		return
	}
	_, rows, err := s.credentials(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if s.Scans != nil {
		fails := s.Scans.Status().AuthFailures
		for i := range rows {
			for _, f := range fails {
				if f.Prefix == rows[i].Prefix && rows[i].Problem == "" {
					t := f.At
					rows[i].AuthFail = &t
				}
			}
		}
	}
	if rc := s.Registries; rc != nil {
		rc.mu.Lock()
		for i := range rows {
			if t, ok := rc.tests[rows[i].Prefix]; ok {
				rows[i].LastTest = &t
			}
		}
		rc.mu.Unlock()
	}
	out := map[string]any{"credentials": rows, "canEdit": s.Registries != nil && s.Registries.Box != nil,
		"scanning": s.Scans != nil, "scansOff": s.ScansOff}
	writeJSON(w, http.StatusOK, out)
}

type registryBody struct {
	Prefix    string `json:"prefix"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	ExpiresAt string `json:"expiresAt"` // YYYY-MM-DD or "" = none
}

func validCredField(v string, max int) bool {
	return v != "" && len(v) <= max && !strings.ContainsFunc(v, unicode.IsControl)
}

// check validates the body; needLogin = username and password are required (add). It writes the error.
func (s *Server) checkRegistryBody(w http.ResponseWriter, b *registryBody, needLogin bool) (*time.Time, bool) {
	if s.Registries == nil || s.Registries.Box == nil {
		httpError(w, http.StatusConflict, "set OWL_SECRET_KEY on the server to manage registry credentials here (openssl rand -base64 32)")
		return nil, false
	}
	p, ok := scan.NormPrefix(b.Prefix)
	if !ok {
		httpError(w, http.StatusBadRequest, "prefix: a registry host or host/path, e.g. registry.gitlab.example.com/team-a")
		return nil, false
	}
	b.Prefix = p
	b.Username = strings.TrimSpace(b.Username)
	if needLogin || b.Username != "" || b.Password != "" {
		if !validCredField(b.Username, 256) {
			httpError(w, http.StatusBadRequest, "username: required, up to 256 characters")
			return nil, false
		}
		if !validCredField(b.Password, 4096) {
			httpError(w, http.StatusBadRequest, "password: required, up to 4096 characters, no line breaks")
			return nil, false
		}
	}
	if b.ExpiresAt == "" {
		return nil, true
	}
	end, err := scan.EndOfDay(b.ExpiresAt)
	if err != nil {
		httpError(w, http.StatusBadRequest, "expiresAt: a date like 2027-03-31, or empty")
		return nil, false
	}
	return &end, true
}

func (s *Server) fileHas(prefix string) bool {
	return slices.ContainsFunc(s.Registries.File, func(c scan.Credential) bool { return c.Prefix == prefix })
}

func actorOf(r *http.Request) string {
	if sess := sessionFrom(r); sess != nil {
		return sess.User.Email
	}
	return ""
}

func (s *Server) addRegistry(w http.ResponseWriter, r *http.Request) {
	if !browserOnly(w, r) {
		return
	}
	var b registryBody
	if !decodeJSON(w, r, &b) {
		return
	}
	exp, ok := s.checkRegistryBody(w, &b, true)
	if !ok {
		return
	}
	if s.fileHas(b.Prefix) {
		httpError(w, http.StatusConflict, b.Prefix+" comes from OWL_REGISTRY_AUTH_FILE: change it there")
		return
	}
	err := s.Store.AddRegistryCredential(r.Context(), store.RegistryCredential{Prefix: b.Prefix, Username: b.Username,
		Secret: s.Registries.Box.Seal([]byte(b.Password), credAD(b.Prefix)), ExpiresAt: exp, UpdatedBy: actorOf(r)})
	if errors.Is(err, store.ErrExists) {
		httpError(w, http.StatusConflict, b.Prefix+" already has a credential: replace it instead")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "registry credential added", "prefix", b.Prefix, "username", b.Username)
	s.afterRegistryChange(r)
	w.WriteHeader(http.StatusCreated)
}

// updateRegistry replaces the login (username and password together) and/or sets the expiry.
func (s *Server) updateRegistry(w http.ResponseWriter, r *http.Request) {
	if !browserOnly(w, r) {
		return
	}
	var b registryBody
	if !decodeJSON(w, r, &b) {
		return
	}
	exp, ok := s.checkRegistryBody(w, &b, false)
	if !ok {
		return
	}
	stored, err := s.Store.ListRegistryCredentials(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	i := slices.IndexFunc(stored, func(c store.RegistryCredential) bool { return c.Prefix == b.Prefix })
	if i < 0 {
		code, msg := http.StatusNotFound, "no credential for "+b.Prefix+" in the UI"
		if s.fileHas(b.Prefix) {
			code, msg = http.StatusConflict, b.Prefix+" comes from OWL_REGISTRY_AUTH_FILE: change it there"
		}
		httpError(w, code, msg)
		return
	}
	c := stored[i]
	c.ExpiresAt, c.UpdatedBy = exp, actorOf(r)
	action := "registry credential expiry changed"
	if b.Password != "" {
		c.Username, c.Secret = b.Username, s.Registries.Box.Seal([]byte(b.Password), credAD(b.Prefix))
		action = "registry credential replaced"
	}
	if err := s.Store.UpdateRegistryCredential(r.Context(), c); errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no credential for "+b.Prefix+" in the UI")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, action, "prefix", b.Prefix, "username", c.Username, "expires", b.ExpiresAt)
	if b.Password != "" {
		s.Registries.forgetTest(b.Prefix)
	}
	s.afterRegistryChange(r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteRegistry(w http.ResponseWriter, r *http.Request) {
	if !browserOnly(w, r) {
		return
	}
	var b struct {
		Prefix string `json:"prefix"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	p, _ := scan.NormPrefix(b.Prefix)
	err := s.Store.DeleteRegistryCredential(r.Context(), p)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no credential for "+p+" in the UI")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "registry credential deleted", "prefix", p)
	if s.Registries != nil {
		s.Registries.forgetTest(p)
	}
	s.afterRegistryChange(r)
	w.WriteHeader(http.StatusNoContent)
}

func (rc *RegistryCreds) forgetTest(prefix string) {
	rc.mu.Lock()
	delete(rc.tests, prefix)
	rc.mu.Unlock()
}

func (s *Server) afterRegistryChange(r *http.Request) {
	if err := s.ReloadCredentials(context.WithoutCancel(r.Context())); err != nil {
		s.Log.Error("registry credentials reload failed", "err", err)
	}
}

// loginTester is TestLogin (a fake in tests).
var loginTester = func(ctx context.Context, c scan.Credential) scan.LoginResult { return scan.TestLogin(ctx, nil, c) }

// testRegistry logs in with a credential in use (from the file or the UI) and says how it went.
func (s *Server) testRegistry(w http.ResponseWriter, r *http.Request) {
	if !browserOnly(w, r) {
		return
	}
	var b struct {
		Prefix string `json:"prefix"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	p, _ := scan.NormPrefix(b.Prefix)
	creds, rows, err := s.credentials(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	i := slices.IndexFunc(creds, func(c scan.Credential) bool { return c.Prefix == p })
	if i < 0 {
		msg := "no credential for " + p
		for _, row := range rows {
			if row.Prefix == p && row.Problem != "" {
				msg = row.Problem
			}
		}
		httpError(w, http.StatusNotFound, msg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res := loginTester(ctx, creds[i])
	if s.Registries != nil {
		s.Registries.mu.Lock()
		if s.Registries.tests == nil {
			s.Registries.tests = map[string]scan.LoginResult{}
		}
		s.Registries.tests[p] = res
		s.Registries.mu.Unlock()
	}
	s.audit(r, "registry credential tested", "prefix", p, "outcome", res.Outcome)
	writeJSON(w, http.StatusOK, res)
}
