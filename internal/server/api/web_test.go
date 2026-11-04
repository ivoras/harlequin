package api

import (
	"io"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPAHandler(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":        {Data: []byte("<html>spa</html>")},
		"m/index.html":      {Data: []byte("<html>mobile</html>")},
		"doc/index.html":    {Data: []byte("<html>doc</html>")},
		"assets/app.js":     {Data: []byte("js")},
		"assets/styles.css": {Data: []byte("css")},
	}
	h := spaHandler(fsys)

	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		body, _ := io.ReadAll(rec.Result().Body)
		return rec.Code, string(body)
	}

	for _, c := range []struct {
		path string
		code int
		want string
	}{
		{"/", 200, "<html>spa</html>"},
		{"/assets/app.js", 200, "js"},
		{"/skills", 200, "<html>spa</html>"},  // client route -> SPA
		{"/assets/", 200, "<html>spa</html>"}, // directory -> SPA
		{"/../../etc/passwd", 400, ""},        // traversal: rejected by net/http itself
		{"/m/", 200, "<html>mobile</html>"},   // mobile UI
		{"/m/skills", 200, "<html>mobile</html>"},
		{"/m", 301, ""},
		{"/doc/p3.17", 200, "<html>doc</html>"}, // standalone document page
		{"/doc/s.12", 200, "<html>doc</html>"},
		{"/documents", 200, "<html>spa</html>"}, // only the exact /doc prefix
		{"/mobile", 200, "<html>spa</html>"},    // only the exact /m/ prefix
	} {
		code, body := get(c.path)
		if code != c.code || (c.want != "" && body != c.want) {
			t.Errorf("GET %s = %d %q, want %d %q", c.path, code, body, c.code, c.want)
		}
	}
}
