package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spaHandler serves the static web UI from fsys (a disk directory in dev, or
// the filesystem embedded in the binary): a real file when the request maps to
// one, otherwise index.html (so client-side routing / deep links work). The
// API lives under /api/v1, which is matched more specifically and never
// reaches here. fs.FS path rules reject traversal outside the root.
func spaHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" {
			if info, err := fs.Stat(fsys, name); err == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// Unknown path (or a directory): hand it to the SPA.
		http.ServeFileFS(w, r, fsys, "index.html")
	})
}
