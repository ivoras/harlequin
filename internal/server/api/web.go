package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// spaHandler serves the static web UI from dir: a real file when the request maps
// to one, otherwise index.html (so client-side routing / deep links work). The
// API lives under /api/v1, which is matched more specifically and never reaches
// here. Path traversal outside dir is rejected.
func spaHandler(dir string) http.Handler {
	index := filepath.Join(dir, "index.html")
	fileServer := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Map the URL path to a file under dir, neutralizing "..".
		clean := filepath.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
		full := filepath.Join(dir, clean)
		if rel, err := filepath.Rel(dir, full); err != nil || strings.HasPrefix(rel, "..") {
			http.ServeFile(w, r, index)
			return
		}
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		// Unknown path (or a directory): hand it to the SPA.
		http.ServeFile(w, r, index)
	})
}
