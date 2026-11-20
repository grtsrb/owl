// Command docsgen renders docs/*.md into internal/docs/dist (HTML pages + search index), which the
// server binary embeds. `make build` and the Dockerfile run it before `go build`:
//
//	go run ./internal/docs/docsgen
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"owl/internal/docs"
	"owl/internal/docs/render"
)

func main() {
	root := flag.String("root", ".", "repository root (holds docs/)")
	out := flag.String("out", "internal/docs/dist", "output directory (replaced, .keep kept)")
	flag.Parse()
	if err := run(*root, *out); err != nil {
		fmt.Fprintln(os.Stderr, "docsgen:", err)
		os.Exit(1)
	}
}

func run(root, out string) error {
	res, err := render.Build(root)
	if err != nil {
		return err
	}
	// Start clean so a removed page doesn't linger in the next build.
	if err := os.RemoveAll(filepath.Join(out, docs.PageDir)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(out, docs.PageDir), 0o755); err != nil {
		return err
	}
	for id, html := range res.HTML {
		if err := os.WriteFile(filepath.Join(out, docs.PageDir, id+".html"), html, 0o644); err != nil {
			return err
		}
	}
	b, err := json.Marshal(res.Index)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, docs.IndexFile), b, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, ".keep"), nil, 0o644); err != nil {
		return err
	}
	fmt.Printf("docsgen: %d pages, %d sections, index %d bytes\n", len(res.Index.Pages), len(res.Index.Sections), len(b))
	return nil
}
