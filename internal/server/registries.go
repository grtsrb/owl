package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"owl/internal/scan"
	"owl/internal/secretbox"
	"owl/internal/store"
)

// Registry logins in the UI (milestones 15.1 + 15.2, DESIGN.md "Registry credentials in the UI" and "Add a
// login where the scan fails"): a login is stored once (write-only password sealed with OWL_SECRET_KEY) and
// used for one or more prefixes; Trivy pulls with the longest matching prefix across all logins. Entries
// from OWL_REGISTRY_AUTH_FILE are listed read-only and win over a UI prefix that's the same.

// RegistryCreds is the server's part: the key and the file's entries (set at start).
type RegistryCreds struct {
	Box  *secretbox.Box   // nil = OWL_SECRET_KEY not set: UI credentials can't be added or used
	File scan.Credentials // OWL_REGISTRY_AUTH_FILE

	mu    sync.Mutex
	tests map[string]loginTest // last Test per prefix (memory only): read access differs per repository
	load  sync.Mutex           // one reload at a time, so the newest list wins
}

type loginTest struct {
	scan.LoginResult
	Prefix string `json:"prefix"`
	key    string // the login it was made with: shown only while that login is used for the prefix
}

func (s *Server) registryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/registries", s.requireAdmin(s.listRegistries))
	mux.HandleFunc("POST /api/v1/registries", s.requireAdmin(s.addRegistry))
	mux.HandleFunc("PUT /api/v1/registries/{id}", s.requireAdmin(s.updateRegistry))
	mux.HandleFunc("DELETE /api/v1/registries/{id}", s.requireAdmin(s.deleteRegistry))
	mux.HandleFunc("POST /api/v1/registries/{id}/scopes", s.requireAdmin(s.addRegistryScope))
	mux.HandleFunc("DELETE /api/v1/registries/scopes", s.requireAdmin(s.deleteRegistryScope))
	mux.HandleFunc("POST /api/v1/registries/test", s.requireAdmin(s.testRegistry))
	mux.HandleFunc("GET /api/v1/registries/suggest", s.requireAdmin(s.suggestRegistryScopes))
}

// credAD binds a sealed password to its login's seal id (a migrated 15.1 login: its old prefix; else
// "login:<random>@<registry host>").
func credAD(sealID string) []byte { return []byte("owl registry credential\x00" + sealID) }

// newSealID binds a new login to its registry host: the password is never sent anywhere else, so an admin
// (or a stolen session) can't add a prefix on another host and press Test to receive it — write-only stays
// write-only. The host is part of the sealed data, so editing seal_id in the database breaks decryption.
func newSealID(host string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "login:" + hex.EncodeToString(b) + "@" + host
}

// sealHost is the registry host a login's password may go to.
func sealHost(sealID string) string {
	if rest, ok := strings.CutPrefix(sealID, "login:"); ok {
		_, host, _ := strings.Cut(rest, "@")
		return host
	}
	return prefixHost(sealID)
}

func prefixHost(prefix string) string {
	host, _, _ := strings.Cut(prefix, "/")
	return host
}

func uiKey(id int64) string        { return "ui:" + strconv.FormatInt(id, 10) }
func fileKey(prefix string) string { return "file:" + prefix }

// loginRow is one login of Settings → Registries. Never carries a password.
type loginRow struct {
	Key       string     `json:"key"` // "ui:<id>" | "file:<prefix>"
	ID        int64      `json:"id,omitempty"`
	Name      string     `json:"name"`
	Username  string     `json:"username"`
	Source    string     `json:"source"` // "ui" | "file"
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy string     `json:"updatedBy,omitempty"`
	Problem   string     `json:"problem,omitempty"` // why the whole login isn't used
	Scopes    []scopeRow `json:"scopes"`
}

type scopeRow struct {
	Prefix   string            `json:"prefix"`
	Problem  string            `json:"problem,omitempty"` // why this prefix isn't used
	AuthFail *time.Time        `json:"authFailedAt,omitempty"`
	Images   int               `json:"images"` // running scannable images this prefix is used for (longest match)
	LastTest *scan.LoginResult `json:"lastTest,omitempty"`
}

// credentials merges the file's entries and the UI's logins (decrypted, one entry per prefix). rows
// describe both for the UI, sorted by name (file entries by prefix).
func (s *Server) credentials(ctx context.Context) (scan.Credentials, []loginRow, error) {
	rc := s.Registries
	if rc == nil {
		rc = &RegistryCreds{}
	}
	stored, err := s.Store.ListRegistryLogins(ctx)
	if err != nil {
		return nil, nil, err
	}
	creds := slices.Clone(rc.File)
	rows := []loginRow{}
	for _, l := range stored {
		row := loginRow{Key: uiKey(l.ID), ID: l.ID, Name: l.Name, Username: l.Username, Source: "ui", ExpiresAt: l.ExpiresAt,
			UpdatedAt: &l.UpdatedAt, UpdatedBy: l.UpdatedBy, Scopes: []scopeRow{}}
		var pw []byte
		switch {
		case rc.Box == nil:
			row.Problem = "not used: OWL_SECRET_KEY isn't set on this server"
		default:
			if pw, err = rc.Box.Open(l.Secret, credAD(l.SealID)); err != nil {
				row.Problem = "can't decrypt (saved with another OWL_SECRET_KEY) — replace the password"
			}
		}
		for _, p := range l.Scopes {
			sr := scopeRow{Prefix: p}
			if rc.fileHas(p) {
				sr.Problem = "not used: OWL_REGISTRY_AUTH_FILE has the same prefix (the file wins)"
			} else if h := sealHost(l.SealID); prefixHost(p) != h {
				sr.Problem = "not used: this login is for " + h + " only"
			} else if row.Problem == "" {
				creds = append(creds, scan.Credential{Prefix: p, Username: l.Username, Password: string(pw), ExpiresAt: l.ExpiresAt, Login: l.Name})
			}
			row.Scopes = append(row.Scopes, sr)
		}
		rows = append(rows, row)
	}
	for _, c := range rc.File {
		rows = append(rows, loginRow{Key: fileKey(c.Prefix), Name: c.Prefix, Username: c.Username, Source: "file",
			ExpiresAt: c.ExpiresAt, Scopes: []scopeRow{{Prefix: c.Prefix}}})
	}
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
			s.Log.Warn("registry login not used", "login", r.Name, "why", r.Problem)
		}
		for _, sc := range r.Scopes {
			if sc.Problem != "" {
				s.Log.Warn("registry login prefix not used", "login", r.Name, "prefix", sc.Prefix, "why", sc.Problem)
			}
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
	ctx := r.Context()
	creds, rows, err := s.credentials(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Which prefix each running image pulls with (longest match), and the prefixes that failed to log in.
	used := map[string]int{}
	var fails []scan.AuthFailure
	if s.Scans != nil {
		if ts, err := s.ScanTargets(ctx); err == nil {
			for _, t := range ts {
				if c := creds.For(t.Ref); c != nil {
					used[c.Prefix]++
				}
			}
		}
		fails = s.Scans.Status().AuthFailures
	}
	var tests map[string]loginTest
	if rc := s.Registries; rc != nil {
		rc.mu.Lock()
		tests = maps(rc.tests)
		rc.mu.Unlock()
	}
	for i := range rows {
		for j := range rows[i].Scopes {
			sc := &rows[i].Scopes[j]
			if t, ok := tests[sc.Prefix]; ok && t.key == rows[i].Key {
				sc.LastTest = &t.LoginResult
			}
			if sc.Problem != "" || rows[i].Problem != "" {
				continue
			}
			sc.Images = used[sc.Prefix]
			for _, f := range fails {
				if f.Prefix == sc.Prefix {
					t := f.At
					sc.AuthFail = &t
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"logins": rows, "canEdit": s.Registries != nil && s.Registries.Box != nil,
		"scanning": s.Scans != nil, "scansOff": s.ScansOff})
}

func maps[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

type registryBody struct {
	Name      string `json:"name"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	ExpiresAt string `json:"expiresAt"` // YYYY-MM-DD or "" = none
	Prefix    string `json:"prefix"`    // add only: the first prefix it's used for
}

func validCredField(v string, max int) bool {
	return v != "" && len(v) <= max && !strings.ContainsFunc(v, unicode.IsControl)
}

func (s *Server) canSeal(w http.ResponseWriter) bool {
	if s.Registries == nil || s.Registries.Box == nil {
		httpError(w, http.StatusConflict, "set OWL_SECRET_KEY on the server to manage registry logins here (openssl rand -base64 32)")
		return false
	}
	return true
}

// checkPrefix normalizes a prefix a UI login may be used for. It writes the error.
func (s *Server) checkPrefix(w http.ResponseWriter, raw string) (string, bool) {
	p, ok := scan.NormPrefix(raw)
	if !ok {
		httpError(w, http.StatusBadRequest, "prefix: a registry host or host/path, e.g. registry.gitlab.example.com/team-a")
		return "", false
	}
	// A login's host is allowed to be pulled from (registryRefused) and Test sends the password there: the
	// same hosts as for in-house registries are refused.
	if host, _, _ := strings.Cut(p, "/"); scan.NeverRegistry(host) != "" {
		httpError(w, http.StatusBadRequest, "prefix: "+scan.NeverRegistry(host))
		return "", false
	}
	if s.Registries.fileHas(p) {
		httpError(w, http.StatusConflict, p+" comes from OWL_REGISTRY_AUTH_FILE: change it there")
		return "", false
	}
	return p, true
}

// checkLogin validates name, username/password and expiry; needLogin = username and password are required
// (add). It writes the error.
func checkLogin(w http.ResponseWriter, b *registryBody, needLogin bool) (*time.Time, bool) {
	b.Name, b.Username = strings.TrimSpace(b.Name), strings.TrimSpace(b.Username)
	if b.Name != "" && !validCredField(b.Name, 100) {
		httpError(w, http.StatusBadRequest, "name: up to 100 characters")
		return nil, false
	}
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

func (rc *RegistryCreds) fileHas(prefix string) bool {
	return rc != nil && slices.ContainsFunc(rc.File, func(c scan.Credential) bool { return c.Prefix == prefix })
}

func actorOf(r *http.Request) string {
	if sess := sessionFrom(r); sess != nil {
		return sess.User.Email
	}
	return ""
}

// addRegistry stores a new login with its first prefix.
func (s *Server) addRegistry(w http.ResponseWriter, r *http.Request) {
	if !browserOnly(w, r) {
		return
	}
	var b registryBody
	if !decodeJSON(w, r, &b) || !s.canSeal(w) {
		return
	}
	p, ok := s.checkPrefix(w, b.Prefix)
	if !ok {
		return
	}
	exp, ok := checkLogin(w, &b, true)
	if !ok {
		return
	}
	if b.Name == "" { // with the prefix: unique (a prefix has one login), also for one username per group
		b.Name = b.Username + " @ " + p
	}
	seal := newSealID(prefixHost(p))
	id, err := s.Store.AddRegistryLogin(r.Context(), store.RegistryLogin{Name: b.Name, Username: b.Username,
		Secret: s.Registries.Box.Seal([]byte(b.Password), credAD(seal)), SealID: seal, ExpiresAt: exp,
		UpdatedBy: actorOf(r), Scopes: []string{p}})
	if errors.Is(err, store.ErrExists) {
		httpError(w, http.StatusConflict, "a login named "+strconv.Quote(b.Name)+" or one for "+p+" already exists: pick another name, or use that login")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "registry login added", "login", b.Name, "username", b.Username, "prefix", p)
	s.afterRegistryChange(r)
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

// uiLogin finds the UI login of the {id} path value. It writes the error.
func (s *Server) uiLogin(w http.ResponseWriter, r *http.Request) (store.RegistryLogin, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpError(w, http.StatusBadRequest, "id: a login id")
		return store.RegistryLogin{}, false
	}
	stored, err := s.Store.ListRegistryLogins(r.Context())
	if err != nil {
		s.fail(w, err)
		return store.RegistryLogin{}, false
	}
	i := slices.IndexFunc(stored, func(l store.RegistryLogin) bool { return l.ID == id })
	if i < 0 {
		httpError(w, http.StatusNotFound, "no such registry login")
		return store.RegistryLogin{}, false
	}
	return stored[i], true
}

// updateRegistry renames the login, replaces its username and password (together) and/or sets the expiry.
func (s *Server) updateRegistry(w http.ResponseWriter, r *http.Request) {
	if !browserOnly(w, r) {
		return
	}
	var b registryBody
	if !decodeJSON(w, r, &b) || !s.canSeal(w) {
		return
	}
	exp, ok := checkLogin(w, &b, false)
	if !ok {
		return
	}
	l, ok := s.uiLogin(w, r)
	if !ok {
		return
	}
	old := l.Name
	if b.Name != "" {
		l.Name = b.Name
	}
	l.ExpiresAt, l.UpdatedBy = exp, actorOf(r)
	action := "registry login changed"
	if b.Password != "" {
		l.Username, l.Secret = b.Username, s.Registries.Box.Seal([]byte(b.Password), credAD(l.SealID))
		action = "registry login replaced"
	}
	if err := s.Store.UpdateRegistryLogin(r.Context(), l); errors.Is(err, store.ErrExists) {
		httpError(w, http.StatusConflict, "a login named "+strconv.Quote(l.Name)+" already exists")
		return
	} else if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no such registry login")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, action, "login", l.Name, "was", old, "username", l.Username, "expires", b.ExpiresAt)
	if b.Password != "" {
		s.Registries.forgetTest(uiKey(l.ID))
	}
	s.afterRegistryChange(r)
	w.WriteHeader(http.StatusNoContent)
}

// deleteRegistry removes a login and every prefix it was used for.
func (s *Server) deleteRegistry(w http.ResponseWriter, r *http.Request) {
	if !browserOnly(w, r) {
		return
	}
	l, ok := s.uiLogin(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteRegistryLogin(r.Context(), l.ID); errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no such registry login")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "registry login deleted", "login", l.Name, "prefixes", strings.Join(l.Scopes, ","))
	if s.Registries != nil {
		s.Registries.forgetTest(uiKey(l.ID))
	}
	s.afterRegistryChange(r)
	w.WriteHeader(http.StatusNoContent)
}

// addRegistryScope uses an existing login for one more prefix.
func (s *Server) addRegistryScope(w http.ResponseWriter, r *http.Request) {
	if !browserOnly(w, r) {
		return
	}
	var b struct {
		Prefix string `json:"prefix"`
	}
	if !decodeJSON(w, r, &b) || !s.canSeal(w) {
		return
	}
	p, ok := s.checkPrefix(w, b.Prefix)
	if !ok {
		return
	}
	l, ok := s.uiLogin(w, r)
	if !ok {
		return
	}
	if h := sealHost(l.SealID); prefixHost(p) != h {
		httpError(w, http.StatusBadRequest, strconv.Quote(l.Name)+" is a login for "+h+": it's never sent to another registry (add a new login for "+prefixHost(p)+")")
		return
	}
	if err := s.Store.AddRegistryScope(r.Context(), l.ID, p); errors.Is(err, store.ErrExists) {
		httpError(w, http.StatusConflict, p+" already has a login: remove it there first")
		return
	} else if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no such registry login")
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "registry login used for a prefix", "login", l.Name, "prefix", p)
	s.afterRegistryChange(r)
	w.WriteHeader(http.StatusNoContent)
}

// deleteRegistryScope stops using a login for a prefix; a login's last prefix can't be removed (delete
// the login instead — a login used for nothing is easy to forget).
func (s *Server) deleteRegistryScope(w http.ResponseWriter, r *http.Request) {
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
	stored, err := s.Store.ListRegistryLogins(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	i := slices.IndexFunc(stored, func(l store.RegistryLogin) bool { return slices.Contains(l.Scopes, p) })
	if i < 0 {
		httpError(w, http.StatusNotFound, "no UI login is used for "+p)
		return
	}
	if len(stored[i].Scopes) == 1 {
		httpError(w, http.StatusConflict, p+" is the only prefix of "+strconv.Quote(stored[i].Name)+": delete the login instead")
		return
	}
	if err := s.Store.DeleteRegistryScope(r.Context(), p); errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no UI login is used for "+p)
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "registry login no longer used for a prefix", "login", stored[i].Name, "prefix", p)
	s.afterRegistryChange(r)
	w.WriteHeader(http.StatusNoContent)
}

// forgetTest drops the test results made with a login (its password changed, or it's gone).
func (rc *RegistryCreds) forgetTest(key string) {
	rc.mu.Lock()
	for p, t := range rc.tests {
		if t.key == key {
			delete(rc.tests, p)
		}
	}
	rc.mu.Unlock()
}

func (s *Server) afterRegistryChange(r *http.Request) {
	if err := s.ReloadCredentials(context.WithoutCancel(r.Context())); err != nil {
		s.Log.Error("registry credentials reload failed", "err", err)
	}
}

// loginTester is TestLogin (a fake in tests).
var loginTester = func(ctx context.Context, c scan.Credential, repo string) scan.LoginResult {
	return scan.TestLogin(ctx, nil, c, repo)
}

// testRegistry logs in with the login used for a prefix (from the file or the UI) and says how it went.
// The prefix matters: GitLab checks the pull scope (repository path) when it hands out a token.
func (s *Server) testRegistry(w http.ResponseWriter, r *http.Request) {
	if !browserOnly(w, r) {
		return
	}
	var b struct {
		Prefix string `json:"prefix"`
		Ref    string `json:"ref"` // optional: the image Add login was opened on — its read access is checked
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
		msg := "no login is used for " + p
		for _, row := range rows {
			for _, sc := range row.Scopes {
				if sc.Prefix == p {
					msg = cmpOr(sc.Problem, row.Problem, msg)
				}
			}
		}
		httpError(w, http.StatusNotFound, msg)
		return
	}
	key := fileKey(p)
	for _, row := range rows {
		if row.Source == "ui" && slices.ContainsFunc(row.Scopes, func(sc scopeRow) bool { return sc.Prefix == p && sc.Problem == "" }) {
			key = row.Key
		}
	}
	// The token's read access is checked on a real repository (a group path like "devops" is never one, so a
	// registry grants nothing on it): the image Add login was opened on, else a running image that pulls with
	// this prefix — one that failed to log in first (it's what the login is for).
	repo := ""
	if b.Ref != "" && creds.For(b.Ref) != nil && creds.For(b.Ref).Prefix == p {
		_, repo, _ = strings.Cut(scan.RepositoryOf(b.Ref), "/")
	}
	if repo == "" && s.Scans != nil {
		if ts, err := s.ScanTargets(r.Context()); err == nil {
			results := s.Scans.Results()
			best := -1
			for _, t := range ts {
				if c := creds.For(t.Ref); c == nil || c.Prefix != p {
					continue
				}
				rank := 0
				if st := results[t.Digest]; st != nil && scan.IsAuthFailure(st.Error) {
					rank = 1
				}
				if rank > best {
					best = rank
					_, repo, _ = strings.Cut(scan.RepositoryOf(t.Ref), "/")
				}
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res := loginTest{LoginResult: loginTester(ctx, creds[i], repo), Prefix: p, key: key}
	if s.Registries != nil {
		s.Registries.mu.Lock()
		if s.Registries.tests == nil {
			s.Registries.tests = map[string]loginTest{}
		}
		s.Registries.tests[p] = res
		s.Registries.mu.Unlock()
	}
	s.audit(r, "registry login tested", "login", creds[i].Label(), "prefix", p, "outcome", res.Outcome)
	writeJSON(w, http.StatusOK, res)
}

func cmpOr(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// scopeOption is one choice of "Add login" for an image: a prefix along its path.
type scopeOption struct {
	Prefix    string `json:"prefix"`
	Failing   int    `json:"failing"`             // images that failed to log in and would pull with this prefix
	UsedBy    string `json:"usedBy,omitempty"`    // a login already uses this exact prefix
	FromFile  bool   `json:"fromFile,omitempty"`  // ... from OWL_REGISTRY_AUTH_FILE (can't be changed here)
	Suggested bool   `json:"suggested,omitempty"` // the default: the first group level
}

// suggestRegistryScopes lists the prefixes an image's login could be added for: the registry, then each
// path level above the repository (registry.x, registry.x/devops, registry.x/devops/billing for
// registry.x/devops/billing/api), with how many images that failed to log in each would cover.
func (s *Server) suggestRegistryScopes(w http.ResponseWriter, r *http.Request) {
	if !browserOnly(w, r) {
		return
	}
	ref := r.URL.Query().Get("ref")
	repo := scan.RepositoryOf(ref)
	host, path, _ := strings.Cut(repo, "/")
	if repo == "" || path == "" || len(ref) > 1024 {
		httpError(w, http.StatusBadRequest, "ref: an image reference like registry.example.com/team/app:1.0")
		return
	}
	if why := scan.NeverRegistry(host); why != "" {
		httpError(w, http.StatusBadRequest, host+": "+why)
		return
	}
	ctx := r.Context()
	creds, rows, err := s.credentials(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	var failing []string // refs of running images whose last scan failed to log in
	if s.Scans != nil {
		running := map[string]bool{}
		if ts, err := s.ScanTargets(ctx); err == nil {
			for _, t := range ts {
				running[t.Digest] = true
			}
		}
		for d, st := range s.Scans.Results() {
			if running[d] && scan.IsAuthFailure(st.Error) {
				failing = append(failing, st.Ref)
			}
		}
	}
	parts := strings.Split(path, "/")
	opts := []scopeOption{}
	for i := 0; i < len(parts); i++ { // host, host/a, host/a/b … (not the repository itself)
		p := host
		if i > 0 {
			p = host + "/" + strings.Join(parts[:i], "/")
		}
		o := scopeOption{Prefix: p, Suggested: i == 1 || i == 0 && len(parts) == 1}
		with := append(slices.Clone(creds), scan.Credential{Prefix: p})
		for _, f := range failing {
			if c := with.For(f); c != nil && c.Prefix == p {
				o.Failing++
			}
		}
		for _, row := range rows {
			for _, sc := range row.Scopes {
				if sc.Prefix == p {
					o.UsedBy, o.FromFile = row.Name, row.Source == "file"
				}
			}
		}
		opts = append(opts, o)
	}
	current := ""
	if c := creds.For(ref); c != nil {
		current = c.Label()
	}
	writeJSON(w, http.StatusOK, map[string]any{"repository": repo, "options": opts, "current": current})
}

// In-house registries (milestone 15.1): images from them are the team's own (listed, not scored, first on
// the Vulnerabilities page) and may be pulled even when the host is an IP address. The server's list
// (--internal-registries / OWL_INTERNAL_REGISTRIES) is read-only here; the UI's is a setting (restored with
// backups) merged into the catalog by ReloadCatalog.

const (
	settingInternalRegistries = "internalRegistries" // newline-separated hosts (host[:port])
	maxInternalRegistries     = 50
)

func splitRegistries(v string) []string {
	out := []string{}
	for _, r := range strings.Split(v, "\n") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

func (s *Server) internalRegistryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/registries/internal", s.getInternalRegistries)
	mux.HandleFunc("PUT /api/v1/registries/internal", s.requireAdmin(s.putInternalRegistries))
}

func (s *Server) getInternalRegistries(w http.ResponseWriter, r *http.Request) {
	sets, err := s.Store.GetSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	env := []string{}
	if s.Catalog != nil {
		env = append(env, s.Catalog.InternalRegistries...)
	}
	writeJSON(w, http.StatusOK, map[string][]string{"server": env, "ui": splitRegistries(sets[settingInternalRegistries])})
}

// normRegistryHost checks one in-house registry: a host name or IP with an optional port. Local, link-local
// and metadata addresses are refused — they're never a registry, and allowing them would let a leaked agent
// token point scans at the server's own network.
func normRegistryHost(h string) (string, string) {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://"), "/")
	bad := func(why string) (string, string) { return "", h + ": " + why }
	if h == "" || len(h) > 253 {
		return bad("a registry host like registry.gitlab.example.com or 10.0.0.5:5000")
	}
	host, port := h, ""
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.HasSuffix(h, "]") && strings.Count(h, ":") == 1 {
		host, port = h[:i], h[i+1:]
	} else if strings.HasPrefix(h, "[") {
		if j := strings.Index(h, "]"); j > 0 {
			host, port = h[1:j], strings.TrimPrefix(h[j+1:], ":")
		}
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return bad("the port must be a number")
		}
	}
	if why := scan.NeverRegistry(h); why != "" {
		return bad(why)
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return h, ""
	}
	if !reRegistryHost.MatchString(host) {
		return bad("a registry host like registry.gitlab.example.com (no path: the whole registry is in-house)")
	}
	return h, ""
}

var reRegistryHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

func (s *Server) putInternalRegistries(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Registries []string `json:"registries"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if len(b.Registries) > maxInternalRegistries {
		httpError(w, http.StatusBadRequest, "registries: at most 50")
		return
	}
	out := []string{}
	for _, raw := range b.Registries {
		h, problem := normRegistryHost(raw)
		if problem != "" {
			httpError(w, http.StatusBadRequest, problem)
			return
		}
		if !slices.Contains(out, h) && (s.Catalog == nil || !slices.Contains(s.Catalog.InternalRegistries, h)) {
			out = append(out, h)
		}
	}
	if err := s.Store.SetSetting(r.Context(), settingInternalRegistries, strings.Join(out, "\n")); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "in-house registries changed", "registries", strings.Join(out, ","))
	ctx := context.WithoutCancel(r.Context())
	if err := s.ReloadCatalog(ctx); err != nil {
		s.Log.Error("catalog reload failed", "err", err)
	}
	s.kickScans() // an IP-address registry just listed can be scanned now
	writeJSON(w, http.StatusOK, map[string][]string{"ui": out})
}
