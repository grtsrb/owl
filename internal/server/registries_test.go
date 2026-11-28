package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"owl/internal/auth"
	"owl/internal/catalog"
	"owl/internal/scan"
	"owl/internal/secretbox"
	"owl/internal/snapshot"
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
	for _, u := range []string{base, base + "/suggest?ref=registry.gitlab.example.com/team/app:1.0"} {
		if code, body := rawGET(t, u, "Authorization", "Bearer "+tok); code != http.StatusForbidden {
			t.Fatalf("token %s: %d %s", u, code, body)
		}
	}

	// A file prefix can't be added in the UI; bad input is refused.
	if code, out := call(t, "POST", base, `{"prefix":"registry.file.example.com","username":"u","password":"p"}`, true); code != http.StatusConflict {
		t.Fatalf("file prefix: %d %v", code, out)
	}
	for _, b := range []string{`{"prefix":"bad prefix","username":"u","password":"p"}`, `{"prefix":"r.example.com","username":"u","password":""}`,
		`{"prefix":"r.example.com","username":"u","password":"a\nb"}`, `{"prefix":"r.example.com","username":"u","password":"p","expiresAt":"soon"}`,
		`{"prefix":"r.example.com","name":"a\tb","username":"u","password":"p"}`, `{"username":"u","password":"p"}`,
		// a login's host may be pulled from and gets the password on Test: never a local or metadata host
		`{"prefix":"169.254.169.254","username":"u","password":"p"}`, `{"prefix":"localhost:5000/team","username":"u","password":"p"}`,
		`{"prefix":"[fd00:ec2::254]/x","username":"u","password":"p"}`, `{"prefix":"metadata.google.internal","username":"u","password":"p"}`} {
		if code, _ := call(t, "POST", base, b, true); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", b, code)
		}
	}

	// Add: stored sealed, never returned; the failed image is retried at once and now scans.
	code, out := call(t, "POST", base, `{"prefix":"https://Registry.GitLab.example.com/team/","username":"gitlab+deploy-token-1","password":"s3cret-pw","expiresAt":"2099-01-31"}`, true)
	if code != http.StatusCreated {
		t.Fatalf("add: %d %v", code, out)
	}
	id := strconv.Itoa(int(out["id"].(float64)))
	waitFor(t, "retry after the credential was added", func() bool { return scanError(s, appDigest) == "" })
	rows, _ := db.ListRegistryLogins(ctx)
	if len(rows) != 1 || rows[0].Name != "gitlab+deploy-token-1 @ registry.gitlab.example.com" || !slices.Equal(rows[0].Scopes, []string{"registry.gitlab.example.com/team"}) ||
		bytes.Contains(rows[0].Secret, []byte("s3cret-pw")) || rows[0].UpdatedBy != "admin@example.com" || !strings.HasPrefix(rows[0].SealID, "login:") {
		t.Fatalf("stored: %+v", rows)
	}
	code, body := rawGET(t, base)
	if code != 200 || strings.Contains(body, "s3cret-pw") || strings.Contains(body, "file-pw") || !strings.Contains(body, `"source":"file"`) ||
		!strings.Contains(body, `"canEdit":true`) || !strings.Contains(body, "2099-01-31") || !strings.Contains(body, `"images":1`) {
		t.Fatalf("list: %d %s", code, body)
	}
	for _, b := range []string{`{"prefix":"registry.gitlab.example.com/team","username":"u","password":"p"}`, // prefix taken
		`{"prefix":"r.example.com","name":"gitlab+deploy-token-1 @ registry.gitlab.example.com","username":"u","password":"p"}`} { // name taken
		if code, _ := call(t, "POST", base, b, true); code != http.StatusConflict {
			t.Fatalf("add twice %s: %d", b, code)
		}
	}

	// The same login for another prefix; a taken or file prefix is refused; the last prefix can't be removed.
	if code, out := call(t, "POST", base+"/"+id+"/scopes", `{"prefix":"registry.gitlab.example.com/other"}`, true); code != http.StatusNoContent {
		t.Fatalf("add scope: %d %v", code, out)
	}
	if c := s.Scans.Credentials().For("registry.gitlab.example.com/other/x:1"); c == nil || c.Password != "s3cret-pw" || c.Login == "" {
		t.Fatalf("second prefix: %+v", c)
	}
	for _, b := range []string{`{"prefix":"registry.gitlab.example.com/other"}`, `{"prefix":"registry.file.example.com"}`} {
		if code, _ := call(t, "POST", base+"/"+id+"/scopes", b, true); code != http.StatusConflict {
			t.Fatalf("scope %s: %d", b, code)
		}
	}
	if code, _ := call(t, "POST", base+"/999/scopes", `{"prefix":"r.example.com/z"}`, true); code != http.StatusNotFound {
		t.Fatalf("unknown login: %d", code)
	}
	if code, _ := call(t, "DELETE", base+"/scopes", `{"prefix":"registry.gitlab.example.com/other"}`, true); code != http.StatusNoContent {
		t.Fatalf("remove scope: %d", code)
	}
	if code, _ := call(t, "DELETE", base+"/scopes", `{"prefix":"registry.gitlab.example.com/team"}`, true); code != http.StatusConflict {
		t.Fatalf("remove last scope: %d", code)
	}

	// Test uses the decrypted password; the result is listed with the login.
	var tested scan.Credential
	var testedRepo string
	loginTester = func(_ context.Context, c scan.Credential, repo string) scan.LoginResult {
		tested, testedRepo = c, repo
		return scan.LoginResult{Outcome: scan.LoginOK, Message: "login accepted", At: time.Now()}
	}
	t.Cleanup(func() {
		loginTester = func(ctx context.Context, c scan.Credential, repo string) scan.LoginResult {
			return scan.TestLogin(ctx, nil, c, repo)
		}
	})
	if code, out := call(t, "POST", base+"/test", `{"prefix":"registry.gitlab.example.com/team"}`, true); code != 200 || out["outcome"] != "ok" || tested.Password != "s3cret-pw" || testedRepo != "team/app" {
		t.Fatalf("test: %d %v %+v %q", code, out, tested, testedRepo)
	}
	if _, body := rawGET(t, base); !strings.Contains(body, `"lastTest"`) {
		t.Fatalf("no last test: %s", body)
	}
	// A result belongs to the prefix it was tested with (read access differs per repository): a prefix
	// tested with this login, then removed and given to another login, doesn't show the old result.
	_ = call2(t, "POST", base+"/"+id+"/scopes", `{"prefix":"registry.gitlab.example.com/moved"}`)
	_ = call2(t, "POST", base+"/test", `{"prefix":"registry.gitlab.example.com/moved"}`)
	_ = call2(t, "DELETE", base+"/scopes", `{"prefix":"registry.gitlab.example.com/moved"}`)
	_ = call2(t, "POST", base, `{"prefix":"registry.gitlab.example.com/moved","name":"other","username":"u","password":"p"}`)
	shown := []string{}
	for _, l := range call2(t, "GET", base, "")["logins"].([]any) {
		for _, sc := range l.(map[string]any)["scopes"].([]any) {
			if m := sc.(map[string]any); m["lastTest"] != nil {
				shown = append(shown, l.(map[string]any)["name"].(string)+" "+m["prefix"].(string))
			}
		}
	}
	if want := []string{"gitlab+deploy-token-1 @ registry.gitlab.example.com registry.gitlab.example.com/team"}; !slices.Equal(shown, want) {
		t.Fatalf("test results shown for %v, want %v", shown, want)
	}
	for _, l := range call2(t, "GET", base, "")["logins"].([]any) {
		if lm := l.(map[string]any); lm["name"] == "other" {
			_ = call2(t, "DELETE", base+"/"+strconv.Itoa(int(lm["id"].(float64))), "")
		}
	}

	// Expiry + rename only: the password stays. Replace: the new one is used (and the old test result goes).
	if code, out := call(t, "PUT", base+"/"+id, `{"name":"team deploy token","expiresAt":""}`, true); code != http.StatusNoContent {
		t.Fatalf("expiry: %d %v", code, out)
	}
	if c := s.Scans.Credentials().For("registry.gitlab.example.com/team/app@" + appDigest); c == nil || c.Password != "s3cret-pw" || c.ExpiresAt != nil || c.Login != "team deploy token" {
		t.Fatalf("after expiry change: %+v", c)
	}
	if code, out := call(t, "PUT", base+"/"+id, `{"username":"gitlab+deploy-token-2","password":"new-pw"}`, true); code != http.StatusNoContent {
		t.Fatalf("replace: %d %v", code, out)
	}
	if c := s.Scans.Credentials().For("registry.gitlab.example.com/team/app@" + appDigest); c == nil || c.Password != "new-pw" || c.Username != "gitlab+deploy-token-2" {
		t.Fatalf("after replace: %+v", c)
	}
	if _, body := rawGET(t, base); strings.Contains(body, `"lastTest"`) {
		t.Fatal("a replaced credential keeps the old test result")
	}

	// Another key: the login isn't used and says why; without a key nothing can be changed.
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
	if code, _ := call(t, "DELETE", base+"/"+id, "", true); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := call(t, "DELETE", base+"/"+id, "", true); code != http.StatusNotFound {
		t.Fatalf("delete twice: %d", code)
	}
	audit, _ := db.ListAudit(ctx, 100)
	n := 0
	for _, a := range audit {
		if strings.HasPrefix(a.Action, "registry login") {
			n++
		}
		if strings.Contains(a.Details, "pw") {
			t.Fatalf("password in audit: %+v", a)
		}
	}
	if n != 12 { // add, use for a prefix, stop using it, test, (moved: use, test, stop, add, delete), rename, replace, delete
		t.Fatalf("audit entries: %d", n)
	}
}

// A 15.1 login (sealed with its prefix as additional data, migrated with that prefix as seal id) keeps
// working, also for a prefix added later and after its password is replaced.
func TestRegistryLoginMigratedSeal(t *testing.T) {
	_, db, s, _ := setupVulns(t)
	ctx := context.Background()
	box := testBox(t, 1)
	s.Registries = &RegistryCreds{Box: box}
	id, err := db.AddRegistryLogin(ctx, store.RegistryLogin{Name: "old", Username: "u", Secret: box.Seal([]byte("old-pw"), credAD("reg.example.com/team")),
		SealID: "reg.example.com/team", UpdatedBy: "a", Scopes: []string{"reg.example.com/team"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.AddRegistryScope(ctx, id, "reg.example.com/more")
	_ = s.ReloadCredentials(ctx)
	if c := s.Scans.Credentials().For("reg.example.com/more/app:1"); c == nil || c.Password != "old-pw" {
		t.Fatalf("migrated login: %+v", c)
	}
}

// "Add login" from an image that failed to log in: the prefixes along its path, the first group level
// suggested, with how many failing images each would cover and which are already taken.
func TestSuggestRegistryScopes(t *testing.T) {
	srv, db, s, ft := setupVulns(t)
	ctx := context.Background()
	s.Registries = &RegistryCreds{Box: testBox(t, 1)}
	_ = s.ReloadCredentials(ctx)
	ft.mu.Lock()
	ft.password = map[string]string{"registry.gitlab.example.com/team/app": "pw"}
	ft.mu.Unlock()
	appDigest := digestOf("a")
	if _, err := s.Scans.Rescan(ctx, appDigest); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "auth failure", func() bool { return strings.HasPrefix(scanError(s, appDigest), "auth failed") })
	if _, out := call(t, "GET", srv.URL+"/api/v1/vulns", "", true); !strings.Contains(fmt.Sprint(out), "needsLogin:true") {
		t.Fatalf("needsLogin not set: %v", out)
	}

	base := srv.URL + "/api/v1/registries/suggest?ref="
	code, out := call(t, "GET", base+"registry.gitlab.example.com/team/app:1.0", "", true)
	if code != 200 || out["repository"] != "registry.gitlab.example.com/team/app" || out["current"] != "" {
		t.Fatalf("suggest: %d %v", code, out)
	}
	opts := out["options"].([]any)
	got := []string{}
	for _, o := range opts {
		m := o.(map[string]any)
		got = append(got, fmt.Sprintf("%v %v %v", m["prefix"], m["failing"], m["suggested"] == true))
	}
	if want := []string{"registry.gitlab.example.com 1 false", "registry.gitlab.example.com/team 1 true"}; !slices.Equal(got, want) {
		t.Fatalf("options = %v, want %v", got, want)
	}

	// Once a login covers team/, it's named as current and as taken; a broader prefix wouldn't win for it.
	if code, out := call(t, "POST", srv.URL+"/api/v1/registries", `{"prefix":"registry.gitlab.example.com/team","name":"team","username":"u","password":"pw"}`, true); code != http.StatusCreated {
		t.Fatalf("add: %d %v", code, out)
	}
	waitFor(t, "retry", func() bool { return scanError(s, appDigest) == "" })
	_, out = call(t, "GET", base+"registry.gitlab.example.com/team/app:1.0", "", true)
	opts = out["options"].([]any)
	if out["current"] != "team" || opts[1].(map[string]any)["usedBy"] != "team" || opts[0].(map[string]any)["failing"] != 0.0 {
		t.Fatalf("after add: %v", out)
	}
	for _, bad := range []string{"", "a%20b/c", "169.254.169.254/x/y:1", "localhost:5000/a/b"} {
		if code, _ := call(t, "GET", base+bad, "", true); code != http.StatusBadRequest {
			t.Fatalf("%q: %d", bad, code)
		}
	}
	_ = db
}

func TestInternalRegistries(t *testing.T) {
	srv, db, s, _ := setupVulns(t)
	ctx := context.Background()
	base := srv.URL + "/api/v1/registries/internal"
	// setupVulns' server list: registry.gitlab.example.com. Viewers read, only admins change.
	code, out := call(t, "GET", base, "", false)
	if code != 200 || len(out["server"].([]any)) != 1 || len(out["ui"].([]any)) != 0 {
		t.Fatalf("get: %d %v", code, out)
	}
	if code, _ := call(t, "PUT", base, `{"registries":["a.example.com"]}`, false); code != http.StatusForbidden {
		t.Fatalf("viewer put: %d", code)
	}
	for _, bad := range []string{"169.254.169.254", "127.0.0.1:5000", "localhost:5000", "metadata.google.internal", "reg.example.com/team", "a b", "[::1]:5000", "reg.example.com:http", "[fd00:ec2::254]", "100.100.100.200"} {
		if code, _ := call(t, "PUT", base, `{"registries":["`+bad+`"]}`, true); code != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d", bad, code)
		}
	}

	// The image on the IP-literal registry isn't scanned until that registry is listed; listing it (in the
	// UI, no restart) marks it in-house and lets it be scanned. The server's own entry isn't duplicated.
	sn := &snapshot.Snapshot{SchemaVersion: snapshot.SchemaVersion, AgentVersion: "test", Cluster: snapshot.Cluster{
		KubernetesVersion: "v1.33.6+k3s1", Nodes: []snapshot.Node{{Name: "n1", Architecture: "arm64"}}}}
	for _, w := range []struct{ name, image, id string }{
		{"app", "registry.gitlab.example.com/team/app:1.0", "registry.gitlab.example.com/team/app@" + digestOf("a")},
		{"ziti", "10.20.0.5:5000/team/tool:2.1", "10.20.0.5:5000/team/tool@" + digestOf("d")},
	} {
		sn.Workloads = append(sn.Workloads, snapshot.Workload{Kind: "Deployment", Namespace: "seed", Name: w.name, DesiredReplicas: 1,
			Containers: []snapshot.Container{{Name: w.name, Image: w.image}},
			Running:    []snapshot.RunningImage{{Container: w.name, Image: w.image, ImageID: w.id, Pods: 1, Ready: 1}}})
	}
	cs, _ := db.ListClusters(ctx)
	tok, hash, prefix := auth.NewToken()
	_ = db.AddToken(ctx, cs[0].ID, hash, prefix)
	if code := push(t, srv.URL, tok, sn); code != http.StatusAccepted {
		t.Fatalf("push: %d", code)
	}
	if id := s.cat().Identify("10.20.0.5:5000/team/tool:2.1"); id.Status == catalog.StatusInHouse {
		t.Fatal("in-house before it was listed")
	}
	if code, out := call(t, "PUT", base, `{"registries":[" HTTPS://10.20.0.5:5000/ ","registry.gitlab.example.com","10.20.0.5:5000"]}`, true); code != 200 || len(out["ui"].([]any)) != 1 {
		t.Fatalf("put: %d %v", code, out)
	}
	if id := s.cat().Identify("10.20.0.5:5000/team/tool:2.1"); id.Status != catalog.StatusInHouse {
		t.Fatalf("not in-house after listing: %+v", id)
	}
	waitFor(t, "scan of the newly allowed registry", func() bool { return s.Scans.Results()[digestOf("d")] != nil })

	// Kept across a catalog reload (start, restore) and readable back.
	if err := s.ReloadCatalog(ctx); err != nil || s.cat().Identify("10.20.0.5:5000/x/y:1").Status != catalog.StatusInHouse {
		t.Fatalf("after reload: %v", err)
	}
	if _, out := call(t, "GET", base, "", false); out["ui"].([]any)[0] != "10.20.0.5:5000" {
		t.Fatalf("get after put: %v", out)
	}
}

// call2 is call as an admin, failing the test on anything but 2xx.
func call2(t *testing.T, method, url, body string) map[string]any {
	t.Helper()
	code, out := call(t, method, url, body, true)
	if code/100 != 2 {
		t.Fatalf("%s %s: %d %v", method, url, code, out)
	}
	return out
}
