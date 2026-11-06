package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/email"
)

// Without SMTP the server must refuse to e-mail a password — before creating
// or changing anything — rather than "send" it into the log.
func TestPasswordEmailNeedsSMTP(t *testing.T) {
	d, err := db.Open(":memory:", db.System, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	st := auth.NewStore(d)
	owner, _ := st.CreateUser(t.Context(), "o@x.io", "password1", "owner")
	s := &Server{Auth: st, Email: email.New(email.Config{})}

	call := func(method, path string, h http.HandlerFunc, body string, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		req = req.WithContext(auth.ContextWithUser(req.Context(), owner))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		h(w, req)
		return w
	}
	w := call("POST", "/users", s.handleCreateUser, `{"email":"new@x.io","password":"password9","send_email":true}`, "")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "SMTP") {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if users, _ := st.ListUsers(t.Context()); len(users) != 1 {
		t.Fatalf("account was created despite the refused e-mail (%d users)", len(users))
	}
	w = call("PATCH", "/users/1", s.handleUpdateUser, `{"password":"password9","send_email":true}`, "1")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
	if _, _, err := st.Login(t.Context(), "o@x.io", "password1"); err != nil {
		t.Fatalf("password changed despite the refused e-mail: %v", err)
	}
}
