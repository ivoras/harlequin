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
	} {
		code, body := get(c.path)
		if code != c.code || (c.want != "" && body != c.want) {
			t.Errorf("GET %s = %d %q, want %d %q", c.path, code, body, c.code, c.want)
		}
	}
}
