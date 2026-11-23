package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/agent"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// A user downloads only files from their own tmp sandbox, as an attachment of a
// safe type; other users' files and path escapes are not reachable.
func TestGetTmpFile(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "users", "7", ".tmp")
	os.MkdirAll(filepath.Join(mine, "sub"), 0o755)
	os.WriteFile(filepath.Join(mine, "report.pdf"), []byte("%PDF-1.4 hi"), 0o600)
	os.WriteFile(filepath.Join(mine, "sub", "page.html"), []byte("<script>x</script>"), 0o600)
	other := filepath.Join(dir, "users", "8", ".tmp")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "secret.txt"), []byte("not yours"), 0o600)

	s := &Server{Agent: &agent.Agent{DataDir: dir}}
	get := func(uid int64, rel string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/files/tmp/"+rel, nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("*", rel)
		ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
		w := httptest.NewRecorder()
		s.handleGetTmpFile(w, r.WithContext(auth.ContextWithUser(ctx, &types.User{ID: uid})))
		return w
	}
	if w := get(7, "report.pdf"); w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" ||
		w.Header().Get("Content-Disposition") != `attachment; filename=report.pdf` || w.Body.String() != "%PDF-1.4 hi" {
		t.Fatalf("pdf: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	if w := get(7, "sub/page.html"); w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("html must not be served as html: %v", w.Header())
	}
	for _, rel := range []string{"nope.pdf", "../../8/.tmp/secret.txt", "..%2f..%2f8%2f.tmp%2fsecret.txt", ""} {
		if w := get(7, rel); w.Code == http.StatusOK {
			t.Errorf("%q served: %q", rel, w.Body.String())
		}
	}
	if w := get(8, "report.pdf"); w.Code == http.StatusOK {
		t.Error("another user's file was served")
	}
}
