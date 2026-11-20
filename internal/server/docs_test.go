package server

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDocs(t *testing.T) {
	e := newSigninEnv(t)
	e.app.Version = "1.2.3"
	e.app.Docs = fstest.MapFS{
		"index.json":         {Data: []byte(`{"pages":[{"id":"install","title":"Installation","description":"How."}],"sections":[{"page":"install","anchor":"run","heading":"Run","text":"docker compose up"}]}`)},
		"pages/install.html": {Data: []byte(`<h1 id="installation">Installation</h1>`)},
		"pages/secret.html":  {Data: []byte(`not listed`)},
	}

	// Signed in only: the docs are never public.
	for _, p := range []string{"/api/v1/docs", "/api/v1/docs/install"} {
		if code, _ := doJSON(t, &http.Client{}, "GET", e.srv.URL+p, ""); code != 401 {
			t.Fatalf("anonymous %s: %d", p, code)
		}
	}
	viewer := asViewer(t, e.srv.URL)
	code, out := doJSON(t, viewer, "GET", e.srv.URL+"/api/v1/docs", "")
	pages, _ := out["pages"].([]any)
	sections, _ := out["sections"].([]any)
	if code != 200 || out["version"] != "1.2.3" || len(pages) != 1 || len(sections) != 1 {
		t.Fatalf("index: %d %v", code, out)
	}
	code, out = doJSON(t, viewer, "GET", e.srv.URL+"/api/v1/docs/install", "")
	if code != 200 || out["title"] != "Installation" || !strings.Contains(out["html"].(string), `id="installation"`) {
		t.Fatalf("page: %d %v", code, out)
	}
	// Only listed pages are served; nothing outside pages/.
	for _, p := range []string{"secret", "..%2findex.json", "nope"} {
		if code, _ := doJSON(t, viewer, "GET", e.srv.URL+"/api/v1/docs/"+p, ""); code != 404 {
			t.Fatalf("%s: %d", p, code)
		}
	}
	// A read-only API token may read them.
	tok, _ := newToken(t, viewer, e.srv.URL, `{"name":"docs"}`)
	if code, _ := doJSON(t, &http.Client{}, "GET", e.srv.URL+"/api/v1/docs/install", "", bearer(tok)...); code != 200 {
		t.Fatalf("token: %d", code)
	}
}

func TestDocsNotBuilt(t *testing.T) {
	e := newSigninEnv(t)
	e.app.Docs = fstest.MapFS{".keep": {}}
	code, out := doJSON(t, asViewer(t, e.srv.URL), "GET", e.srv.URL+"/api/v1/docs", "")
	if code != 404 || !strings.Contains(out["error"].(string), "not built") {
		t.Fatalf("not built: %d %v", code, out)
	}
}
