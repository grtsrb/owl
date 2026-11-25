package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"owl/internal/auth"
	"owl/internal/scan"
	"owl/internal/secretbox"
	"owl/internal/store"
)

func testBox(t *testing.T, b byte) *secretbox.Box {
	box, err := secretbox.New(bytes.Repeat([]byte{b}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func rawGET(t *testing.T, url string, header ...string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if len(header) == 2 {
		req.Header.Set(header[0], header[1])
	} else {
		withSession(req, testAdminToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 400 {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}

func scanError(s *Server, digest string) string {
	if st := s.Scans.Results()[digest]; st != nil {
		return st.Error
	}
	return "(none)"
}

func TestRegistryCredentials(t *testing.T) {
	srv, db, s, ft := setupVulns(t)
	ctx := context.Background()
	base := srv.URL + "/api/v1/registries"
	s.Registries = &RegistryCreds{Box: testBox(t, 1), File: scan.Credentials{{Prefix: "registry.file.example.com", Username: "f", Password: "file-pw", FromFile: true}}}
	if err := s.ReloadCredentials(ctx); err != nil {
		t.Fatal(err)
	}

	// The in-house image now needs a login: a rescan fails with "auth failed".
	appDigest := digestOf("a")
	ft.mu.Lock()
	ft.password = map[string]string{"registry.gitlab.example.com/team/app": "s3cret-pw"}
	ft.mu.Unlock()
	if _, err := s.Scans.Rescan(ctx, appDigest); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "auth failure", func() bool { return strings.HasPrefix(scanError(s, appDigest), "auth failed") })

	// Admin only, never for API tokens (even an admin's).
	if code, _ := call(t, "GET", base, "", false); code != http.StatusForbidden {
		t.Fatalf("viewer list: %d", code)
	}
	tok, hash, _ := auth.NewAPIToken()
	if _, err := db.CreateAPIToken(ctx, store.APIToken{UserID: 1, Name: "ci", Scope: store.ScopeWrite}, hash); err != nil {
		t.Fatal(err)
	}
	if code, body := rawGET(t, base, "Authorization", "Bearer "+tok); code != http.StatusForbidden {
		t.Fatalf("token list: %d %s", code, body)
	}

	// A file prefix can't be added in the UI; bad input is refused.
	if code, out := call(t, "POST", base, `{"prefix":"registry.file.example.com","username":"u","password":"p"}`, true); code != http.StatusConflict {
		t.Fatalf("file prefix: %d %v", code, out)
	}
	for _, b := range []string{`{"prefix":"bad prefix","username":"u","password":"p"}`, `{"prefix":"r.example.com","username":"u","password":""}`,
		`{"prefix":"r.example.com","username":"u","password":"a\nb"}`, `{"prefix":"r.example.com","username":"u","password":"p","expiresAt":"soon"}`} {
		if code, _ := call(t, "POST", base, b, true); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", b, code)
		}
	}

	// Add: stored sealed, never returned; the failed image is retried at once and now scans.
	if code, out := call(t, "POST", base, `{"prefix":"https://Registry.GitLab.example.com/team/","username":"gitlab+deploy-token-1","password":"s3cret-pw","expiresAt":"2099-01-31"}`, true); code != http.StatusCreated {
		t.Fatalf("add: %d %v", code, out)
	}
	waitFor(t, "retry after the credential was added", func() bool { return scanError(s, appDigest) == "" })
	rows, _ := db.ListRegistryCredentials(ctx)
	if len(rows) != 1 || rows[0].Prefix != "registry.gitlab.example.com/team" || bytes.Contains(rows[0].Secret, []byte("s3cret-pw")) || rows[0].UpdatedBy != "admin@example.com" {
		t.Fatalf("stored: %+v", rows)
	}
	code, body := rawGET(t, base)
	if code != 200 || strings.Contains(body, "s3cret-pw") || strings.Contains(body, "file-pw") || !strings.Contains(body, `"source":"file"`) ||
		!strings.Contains(body, `"canEdit":true`) || !strings.Contains(body, "2099-01-31") {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, _ := call(t, "POST", base, `{"prefix":"registry.gitlab.example.com/team","username":"u","password":"p"}`, true); code != http.StatusConflict {
		t.Fatalf("add twice: %d", code)
	}

	// Test uses the decrypted password; the result is listed.
	var tested scan.Credential
	loginTester = func(_ context.Context, c scan.Credential) scan.LoginResult {
		tested = c
		return scan.LoginResult{Outcome: scan.LoginOK, Message: "login accepted", At: time.Now()}
	}
	t.Cleanup(func() {
		loginTester = func(ctx context.Context, c scan.Credential) scan.LoginResult { return scan.TestLogin(ctx, nil, c) }
	})
	if code, out := call(t, "POST", base+"/test", `{"prefix":"registry.gitlab.example.com/team"}`, true); code != 200 || out["outcome"] != "ok" || tested.Password != "s3cret-pw" {
		t.Fatalf("test: %d %v %+v", code, out, tested)
	}
	if _, body := rawGET(t, base); !strings.Contains(body, `"lastTest"`) {
		t.Fatalf("no last test: %s", body)
	}

	// Expiry only: the password stays. Replace: the new one is used (and a wrong one fails the next scan).
	if code, out := call(t, "PUT", base, `{"prefix":"registry.gitlab.example.com/team","expiresAt":""}`, true); code != http.StatusNoContent {
		t.Fatalf("expiry: %d %v", code, out)
	}
	if c := s.Scans.Credentials().For("registry.gitlab.example.com/team/app@" + appDigest); c == nil || c.Password != "s3cret-pw" || c.ExpiresAt != nil {
		t.Fatalf("after expiry change: %+v", c)
	}
	if code, out := call(t, "PUT", base, `{"prefix":"registry.gitlab.example.com/team","username":"gitlab+deploy-token-2","password":"new-pw"}`, true); code != http.StatusNoContent {
		t.Fatalf("replace: %d %v", code, out)
	}
	if c := s.Scans.Credentials().For("registry.gitlab.example.com/team/app@" + appDigest); c == nil || c.Password != "new-pw" || c.Username != "gitlab+deploy-token-2" {
		t.Fatalf("after replace: %+v", c)
	}
	if _, body := rawGET(t, base); strings.Contains(body, `"lastTest"`) {
		t.Fatal("a replaced credential keeps the old test result")
	}

	// Another key: the row isn't used and says why; without a key nothing can be changed.
	s.Registries.Box = testBox(t, 2)
	_ = s.ReloadCredentials(ctx)
	if _, body := rawGET(t, base); !strings.Contains(body, "can't decrypt") {
		t.Fatalf("other key: %s", body)
	}
	if c := s.Scans.Credentials().For("registry.gitlab.example.com/team/app"); c != nil {
		t.Fatal("undecryptable credential used")
	}
	s.Registries.Box = nil
	if code, _ := call(t, "POST", base, `{"prefix":"r.example.com","username":"u","password":"p"}`, true); code != http.StatusConflict {
		t.Fatalf("no key: %d", code)
	}
	s.Registries.Box = testBox(t, 1)

	// Delete; audit entries never carry a password.
	if code, _ := call(t, "DELETE", base, `{"prefix":"registry.gitlab.example.com/team"}`, true); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := call(t, "DELETE", base, `{"prefix":"registry.gitlab.example.com/team"}`, true); code != http.StatusNotFound {
		t.Fatalf("delete twice: %d", code)
	}
	audit, _ := db.ListAudit(ctx, 100)
	n := 0
	for _, a := range audit {
		if strings.HasPrefix(a.Action, "registry credential") {
			n++
		}
		if strings.Contains(a.Details, "pw") {
			t.Fatalf("password in audit: %+v", a)
		}
	}
	if n != 5 {
		t.Fatalf("audit entries: %d", n)
	}
}
