// Package harlequin exposes assets embedded into the server binary: the baked-in
// skills and hats that are deployed to the server data directory on install/update,
// and the built web UI.
package harlequin

import (
	"embed"
	"io/fs"
)

//go:embed skills hats
var bakedFS embed.FS

// BakedFS returns the embedded filesystem rooted at the repository, so the
// assets live under the "skills/" and "hats/" prefixes.
func BakedFS() embed.FS {
	return bakedFS
}

// The built web UI (vite output). "all:" so hashed asset files starting with
// "_" or "." are included too. The tree always contains at least the committed
// .gitkeep, so the directive matches even when the UI hasn't been built.
//
//go:embed all:web/dist
var webFS embed.FS

// EmbeddedWebUI returns the built web UI as a filesystem rooted at its
// index.html, or ok=false when the binary was compiled without a web build
// (web/dist only held the .gitkeep placeholder).
func EmbeddedWebUI() (fs.FS, bool) {
	sub, err := fs.Sub(webFS, "web/dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
