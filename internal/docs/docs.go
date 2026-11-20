// Package docs embeds the user/operator documentation rendered at build time (dist/, produced by
// `go run ./internal/docs/docsgen`, which `make build` and the Dockerfile run). The server binary
// carries only the finished HTML and the search index: no Markdown parser at runtime. Without a
// build the server answers "docs not built"; everything else works.
package docs

import (
	"embed"
	"encoding/json"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// IndexFile and PageDir are the generator's output layout inside dist/.
const (
	IndexFile = "index.json"
	PageDir   = "pages"
)

// Index lists the pages (in reading order) and the search sections of all of them.
type Index struct {
	Pages    []Page    `json:"pages"`
	Sections []Section `json:"sections"`
}

// Page is one served document. ID is the file name without ".md" (also its URL: /docs/<id>).
type Page struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// Section is one search entry: the text under a heading (## or ###), up to the next one. Anchor is
// the heading's id; Level 1 with no anchor = the top of the page, before its first section.
type Section struct {
	Page    string `json:"page"`
	Anchor  string `json:"anchor"`
	Heading string `json:"heading"`
	Level   int    `json:"level"`
	Text    string `json:"text"`
}

// Dist returns the generated files.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Load reads the index from a generated tree; fs.ErrNotExist when the docs weren't built.
func Load(fsys fs.FS) (*Index, error) {
	b, err := fs.ReadFile(fsys, IndexFile)
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return nil, err
	}
	return &ix, nil
}
