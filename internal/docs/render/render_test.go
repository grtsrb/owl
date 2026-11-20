package render

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The real docs must always build: a broken link, raw HTML or a page missing from Order fails here.
func TestRealDocsBuild(t *testing.T) {
	res, err := Build("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Index.Pages) != len(Order) {
		t.Fatalf("pages = %d, want %d", len(res.Index.Pages), len(Order))
	}
	for _, p := range res.Index.Pages {
		if p.Title == "" || p.Description == "" {
			t.Errorf("%s: title %q, description %q", p.ID, p.Title, p.Description)
		}
		if strings.Contains(string(res.HTML[p.ID]), "github.com/grtsrb") {
			t.Errorf("%s links into the repository on GitHub", p.ID)
		}
	}
}

// The UI's "?" links (web/src/dochelp.ts) must open a page and heading that exist.
func TestHelpLinks(t *testing.T) {
	res, err := Build("../../..")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("../../../web/src/dochelp.ts")
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`(?m)^\s+\w+: "([a-z0-9-]+)(?:#([\w-]+))?",`).FindAllStringSubmatch(string(src), -1)
	if len(links) < 8 {
		t.Fatalf("found %d help links in dochelp.ts, want 8", len(links))
	}
	for _, l := range links {
		page, anchor := l[1], l[2]
		found := false
		for _, s := range res.Index.Sections {
			if s.Page == page && (anchor == "" || s.Anchor == anchor) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("help link %s#%s: no such docs page or heading", page, anchor)
		}
	}
}

// repo writes a minimal repository: docs/<id>.md for every page in Order (overridable) plus files.
func repo(t *testing.T, pages map[string]string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range Order {
		s, ok := pages[id]
		if !ok {
			s = "# " + id + "\n\nAbout " + id + ".\n"
		}
		write("docs/"+id+".md", s)
	}
	for _, id := range Excluded {
		write("docs/"+id+".md", "# dev\n")
	}
	for p, s := range files {
		write(p, s)
	}
	return root
}

func TestLinksIncludesAnchors(t *testing.T) {
	root := repo(t, map[string]string{
		"install": "# Installation\n\nFirst paragraph is the description.\n\n" +
			"## Run it\n\nSee [config](configuration.md#limits), [same page](#run-it), [top](concepts.md),\n" +
			"[dev](development.md), [design](../DESIGN.md), [compose](../deploy/compose) and [site](https://example.com).\n\n" +
			"<!-- include deploy/compose/compose.yaml -->\n\n" +
			"```bash\n<!-- include deploy/not/expanded.yaml -->\n```\n",
		"configuration": "# Configuration\n\nText.\n\n## Limits\n\nOWL_MAX_BODY sets it.\n",
	}, map[string]string{"deploy/compose/compose.yaml": "services:\n  owl:\n    image: x\n"})
	res, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	html := string(res.HTML["install"])
	for _, want := range []string{
		`href="/docs/configuration#limits"`, `href="#run-it"`, `href="/docs/concepts"`,
		`<a href="https://example.com" target="_blank" rel="noopener noreferrer">site</a>`,
		`<h2 id="run-it">Run it<a href="#run-it" class="anchor">#</a></h2>`,
		`<code>compose.yaml</code>`, `<code class="language-yaml">services:`,
		`&lt;!-- include deploy/not/expanded.yaml --&gt;`, // inside a code block: left alone
		"[dev]", // not served: text only
	} {
		if want == "[dev]" {
			if !strings.Contains(html, "dev, design, compose") {
				t.Errorf("repo links not turned into text:\n%s", html)
			}
			continue
		}
		if !strings.Contains(html, want) {
			t.Errorf("missing %s in:\n%s", want, html)
		}
	}
	for _, bad := range []string{"development", "DESIGN.md", "deploy/compose\""} {
		if strings.Contains(html, bad) {
			t.Errorf("%q still linked:\n%s", bad, html)
		}
	}
	var page = res.Index.Pages[2]
	if page.ID != "install" || page.Title != "Installation" || page.Description != "First paragraph is the description." {
		t.Errorf("page = %+v", page)
	}
	var found bool
	for _, s := range res.Index.Sections {
		if s.Page == "configuration" && s.Anchor == "limits" {
			found = strings.Contains(s.Text, "OWL_MAX_BODY") && s.Heading == "Limits"
		}
		if s.Page == "install" && s.Anchor == "run-it" && !strings.Contains(s.Text, "services: owl:") {
			t.Errorf("included file not searchable: %q", s.Text)
		}
	}
	if !found {
		t.Errorf("limits section missing: %+v", res.Index.Sections)
	}
}

func TestRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		pages map[string]string
		want  string
	}{
		"raw html":       {map[string]string{"cli": "# CLI\n\nUse <target> here.\n"}, "raw HTML"},
		"html block":     {map[string]string{"cli": "# CLI\n\n<div>x</div>\n"}, "raw HTML"},
		"image":          {map[string]string{"cli": "# CLI\n\n![logo](owl-logo.svg)\n"}, "image"},
		"broken anchor":  {map[string]string{"cli": "# CLI\n\n[x](api.md#nope)\n"}, "no such heading"},
		"broken local":   {map[string]string{"cli": "# CLI\n\n[x](#nope)\n"}, "no such heading"},
		"missing title":  {map[string]string{"cli": "Just text.\n"}, "no # title"},
		"bad include":    {map[string]string{"cli": "# CLI\n\n<!-- include ../etc/passwd -->\n"}, "inside the repository"},
		"absent include": {map[string]string{"cli": "# CLI\n\n<!-- include deploy/x.yaml -->\n"}, "include"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Build(repo(t, tc.pages, nil))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	t.Run("unlisted page", func(t *testing.T) {
		root := repo(t, nil, map[string]string{"docs/new-page.md": "# New\n"})
		if _, err := Build(root); err == nil || !strings.Contains(err.Error(), "render.Order") {
			t.Fatalf("err = %v", err)
		}
	})
}
