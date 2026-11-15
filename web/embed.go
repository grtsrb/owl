// Package web embeds the built UI (web/dist, produced by `make ui`). Without a build the server
// serves a short "UI not built" page; the API works either way.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built UI files.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
