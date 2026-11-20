// Package render turns docs/*.md into the HTML pages and search index the server embeds
// (internal/docs). It runs at build time only (docsgen, tests): the server binary never imports it.
//
// What it does beyond plain Markdown:
//   - links between served pages become /docs/<id>#anchor; links to anything else in the repository
//     (development.md, DESIGN.md, deploy/…) become plain text — the docs in Owl never point into the
//     repository; external http(s) links open in a new tab;
//   - `<!-- include <repo path> -->` on a line of its own is replaced by that file as a code block, so
//     the docs show the exact files of the build (raw HTML is otherwise dropped);
//   - every section heading gets an id and a "#" link (class "anchor") to share it;
//   - images are refused (the UI's CSP and the "docs are text" decision), so are unknown includes.
package render

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"

	"owl/internal/docs"
)

// Order is the reading order of the served pages. Every docs/*.md must be here or in Excluded, so a
// new page can't be forgotten (Build fails).
var Order = []string{
	"user-guide", "concepts", "install", "configuration", "operations", "security",
	"troubleshooting", "catalog", "cli", "api", "changelog",
}

// Excluded pages stay in the repository only (contributors).
var Excluded = []string{"development"}

// Result is everything docsgen writes: the index and one HTML fragment per page.
type Result struct {
	Index docs.Index
	HTML  map[string][]byte
}

var includeRe = regexp.MustCompile(`^<!--\s*include\s+(\S+)\s*-->\s*$`)

// Build renders every served page of root/docs. root is the repository root (for includes).
func Build(root string) (*Result, error) {
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, id := range Order {
		known[id] = true
	}
	for _, id := range Excluded {
		known[id] = true
	}
	present := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".md")
		if !known[id] {
			return nil, fmt.Errorf("docs/%s: add it to render.Order (served) or render.Excluded", e.Name())
		}
		present[id] = true
	}
	served := map[string]bool{}
	for _, id := range Order {
		if !present[id] {
			return nil, fmt.Errorf("docs/%s.md is in render.Order but missing", id)
		}
		served[id] = true
	}
	anchors := map[string]map[string]bool{}
	refs := map[string][]string{}
	res := &Result{HTML: map[string][]byte{}, Index: docs.Index{Pages: []docs.Page{}, Sections: []docs.Section{}}}
	for _, id := range Order {
		src, err := os.ReadFile(filepath.Join(root, "docs", id+".md"))
		if err != nil {
			return nil, err
		}
		anchors[id] = map[string]bool{}
		var pageRefs []string
		page, sections, html, err := renderPage(root, id, src, served, anchors[id], &pageRefs)
		refs[id] = pageRefs
		if err != nil {
			return nil, fmt.Errorf("docs/%s.md: %w", id, err)
		}
		res.Index.Pages = append(res.Index.Pages, page)
		res.Index.Sections = append(res.Index.Sections, sections...)
		res.HTML[id] = html
	}
	// Every internal link must land on a page and heading that exist.
	for _, from := range Order {
		for _, ref := range refs[from] {
			target, frag, _ := strings.Cut(strings.TrimPrefix(ref, "/docs/"), "#")
			if frag != "" && !anchors[target][frag] {
				return nil, fmt.Errorf("docs/%s.md: link to %s.md#%s: no such heading", from, target, frag)
			}
		}
	}
	return res, nil
}

// rawText is the source of a raw HTML node, inline or block (for error messages).
func rawText(n ast.Node, src []byte) string {
	if r, ok := n.(*ast.RawHTML); ok {
		var b strings.Builder
		for i := 0; i < r.Segments.Len(); i++ {
			seg := r.Segments.At(i)
			b.Write(seg.Value(src))
		}
		return b.String()
	}
	return string(n.Lines().Value(src))
}

func newMarkdown() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			// GFM without its table default: alignment as an attribute, not style="" (CSP).
			extension.NewTable(extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute)),
			extension.Strikethrough, extension.TaskList,
		),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		// No html.WithUnsafe: raw HTML in a page is dropped, never passed through.
	)
}

// renderPage renders one page and splits it into search sections.
// ids collects the page's heading ids, refs the internal links it makes (/docs/<id>[#anchor]).
func renderPage(root, id string, src []byte, served map[string]bool, ids map[string]bool, refs *[]string) (docs.Page, []docs.Section, []byte, error) {
	src, err := expandIncludes(root, src)
	if err != nil {
		return docs.Page{}, nil, nil, err
	}
	md := newMarkdown()
	doc := md.Parser().Parse(text.NewReader(src))

	page := docs.Page{ID: id}
	var sections []docs.Section
	cur := &docs.Section{Page: id, Level: 1}
	var buf strings.Builder
	flush := func() {
		cur.Text = squash(buf.String())
		if cur.Text != "" || cur.Anchor != "" {
			sections = append(sections, *cur)
		}
		buf.Reset()
	}
	var firstErr error
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		if h, ok := n.(*ast.Heading); ok {
			title := squash(plain(h, src))
			if h.Level == 1 && page.Title == "" {
				page.Title = title
				cur.Heading = title
				continue
			}
			if h.Level <= 3 {
				flush()
				anchor := headingID(h)
				cur = &docs.Section{Page: id, Anchor: anchor, Heading: title, Level: h.Level}
			} else {
				buf.WriteString(title + " ") // #### and deeper: searchable as part of their section
			}
			continue
		}
		if p, ok := n.(*ast.Paragraph); ok && page.Description == "" && page.Title != "" {
			page.Description = clip(squash(plain(p, src)), 200)
		}
		buf.WriteString(plain(n, src))
		buf.WriteString(" ")
	}
	flush()
	if page.Title == "" {
		return page, nil, nil, fmt.Errorf("no # title")
	}

	// Rewrite links, add heading anchors; refuse images and raw HTML. Changes are applied after the
	// walk (unwrapping a link while walking would cut the walk short).
	var unlink []*ast.Link
	var headings []*ast.Heading
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || firstErr != nil {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Image:
			firstErr = fmt.Errorf("image %q: the docs in Owl are text only", v.Destination)
		case *ast.RawHTML, *ast.HTMLBlock:
			firstErr = fmt.Errorf("raw HTML %q: not rendered in Owl (use `code` for <placeholders>)", squash(rawText(n, src)))
		case *ast.Heading:
			if a := headingID(v); a != "" {
				ids[a] = true
				if v.Level >= 2 {
					headings = append(headings, v)
				}
			}
		case *ast.AutoLink:
			if u := string(v.URL(src)); strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
				v.SetAttributeString("target", []byte("_blank"))
				v.SetAttributeString("rel", []byte("noopener noreferrer"))
			}
		case *ast.Link:
			dest, ok := rewriteLink(string(v.Destination), served)
			if !ok {
				unlink = append(unlink, v)
				break
			}
			v.Destination = []byte(dest)
			switch {
			case strings.HasPrefix(dest, "http://"), strings.HasPrefix(dest, "https://"):
				v.SetAttributeString("target", []byte("_blank"))
				v.SetAttributeString("rel", []byte("noopener noreferrer"))
			case strings.HasPrefix(dest, "#"):
				*refs = append(*refs, "/docs/"+id+dest)
			default:
				*refs = append(*refs, dest)
			}
		}
		return ast.WalkContinue, nil
	})
	if firstErr != nil {
		return page, nil, nil, firstErr
	}
	for _, l := range unlink {
		unwrap(l)
	}
	for _, h := range headings {
		l := ast.NewLink()
		l.Destination = []byte("#" + headingID(h))
		l.SetAttributeString("class", []byte("anchor"))
		l.AppendChild(l, ast.NewString([]byte("#")))
		h.AppendChild(h, l)
	}
	var out bytes.Buffer
	if err := md.Renderer().Render(&out, src, doc); err != nil {
		return page, nil, nil, err
	}
	return page, sections, out.Bytes(), nil
}

// rewriteLink maps a Markdown link target to what the served page uses; false = not served
// (render the text only).
func rewriteLink(dest string, served map[string]bool) (string, bool) {
	switch {
	case strings.HasPrefix(dest, "http://"), strings.HasPrefix(dest, "https://"), strings.HasPrefix(dest, "mailto:"):
		return dest, true
	case strings.HasPrefix(dest, "#"):
		return dest, true
	}
	file, frag, _ := strings.Cut(dest, "#")
	p := path.Clean(path.Join("docs", file)) // links are relative to docs/
	if strings.HasPrefix(p, "docs/") && strings.HasSuffix(p, ".md") {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "docs/"), ".md")
		if served[id] {
			if frag != "" {
				return "/docs/" + id + "#" + frag, true
			}
			return "/docs/" + id, true
		}
	}
	return "", false
}

// unwrap replaces a link with its children (its text stays, the link goes).
func unwrap(l *ast.Link) {
	parent := l.Parent()
	for c := l.FirstChild(); c != nil; {
		next := c.NextSibling()
		parent.InsertBefore(parent, l, c)
		c = next
	}
	parent.RemoveChild(parent, l)
}

// expandIncludes replaces `<!-- include path -->` lines with the file as a fenced code block, with
// its file name above it.
func expandIncludes(root string, src []byte) ([]byte, error) {
	var out bytes.Buffer
	fence := "" // the open code fence ("```", "~~~~", …), "" outside one
	for _, line := range strings.SplitAfter(string(src), "\n") {
		t := strings.TrimSpace(line)
		if f := fenceOf(t); f != "" {
			switch {
			case fence == "":
				fence = f
			case f[0] == fence[0] && len(f) >= len(fence) && strings.TrimLeft(t, f[:1]) == "":
				fence = "" // a closing fence: same character, at least as long, nothing after it
			}
		}
		m := includeRe.FindStringSubmatch(strings.TrimRight(line, "\n"))
		if m == nil || fence != "" {
			out.WriteString(line)
			continue
		}
		rel := path.Clean(m[1])
		if path.IsAbs(rel) || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("include %q: must be a path inside the repository", m[1])
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("include %q: %w", m[1], err)
		}
		code := "```"
		for strings.Contains(string(b), code) {
			code += "`"
		}
		body := strings.TrimRight(string(b), "\n")
		fmt.Fprintf(&out, "`%s`\n\n%s%s\n%s\n%s\n", path.Base(rel), code, lang(rel), body, code)
	}
	return out.Bytes(), nil
}

// fenceOf returns the code fence a (trimmed) line starts with — 3 or more ` or ~ — or "".
func fenceOf(t string) string {
	if t == "" || (t[0] != '`' && t[0] != '~') {
		return ""
	}
	n := 0
	for n < len(t) && t[n] == t[0] {
		n++
	}
	if n < 3 {
		return ""
	}
	return t[:n]
}

func lang(p string) string {
	switch {
	case strings.HasSuffix(p, ".yaml"), strings.HasSuffix(p, ".yml"):
		return "yaml"
	case strings.HasSuffix(p, ".env.example"), strings.HasSuffix(p, ".sh"):
		return "bash"
	case strings.HasSuffix(p, ".json"):
		return "json"
	}
	return "text"
}

func headingID(h *ast.Heading) string {
	if v, ok := h.AttributeString("id"); ok {
		if b, ok := v.([]byte); ok {
			return string(b)
		}
	}
	return ""
}

// plain is the text of a node for search: text, code spans and code blocks; no markup.
func plain(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			if c.Type() == ast.TypeBlock {
				b.WriteString(" ")
			}
			return ast.WalkContinue, nil
		}
		switch v := c.(type) {
		case *ast.Text:
			b.Write(v.Segment.Value(src))
			if v.SoftLineBreak() || v.HardLineBreak() {
				b.WriteString(" ")
			}
		case *ast.String:
			b.Write(v.Value)
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			lines := c.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				b.Write(seg.Value(src))
				b.WriteString(" ")
			}
			return ast.WalkSkipChildren, nil
		case *ast.HTMLBlock, *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		case *ast.AutoLink:
			b.Write(v.URL(src))
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// clip shortens s to at most n bytes at a word boundary, with "…".
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndex(s[:n], " ")
	if cut < n/2 {
		cut = n
		for cut > 0 && !utf8.RuneStart(s[cut]) { // never split a character
			cut--
		}
	}
	return strings.TrimRight(s[:cut], " ,;:—-") + "…"
}
