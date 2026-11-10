package mcp

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestWellKnown(t *testing.T) {
	got := wellKnown("https://ex.com/v1/mcp/", "oauth-protected-resource")
	want := []string{"https://ex.com/.well-known/oauth-protected-resource/v1/mcp", "https://ex.com/.well-known/oauth-protected-resource"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := wellKnown("https://ex.com", "x"); !reflect.DeepEqual(got, []string{"https://ex.com/.well-known/x"}) {
		t.Fatalf("got %v", got)
	}
}

func TestGrantLost(t *testing.T) {
	resp := func(code int) *http.Response { return &http.Response{StatusCode: code} }
	cases := []struct {
		err  error
		want bool
	}{
		{&oauth2.RetrieveError{ErrorCode: "invalid_grant", Response: resp(400)}, true},
		{fmt.Errorf("wrapped: %w", &oauth2.RetrieveError{Response: resp(401)}), true},
		{&oauth2.RetrieveError{Response: resp(503)}, false}, // outage: keep the token
		{errors.New("dial tcp: timeout"), false},
	}
	for i, c := range cases {
		if got := grantLost(c.err); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}

func TestDescribeTokenErr(t *testing.T) {
	err := fmt.Errorf("w: %w", &oauth2.RetrieveError{
		Response:         &http.Response{StatusCode: 400},
		ErrorCode:        "invalid_grant",
		ErrorDescription: "refresh token reuse",
		Body:             []byte("{\"error\":\"invalid_grant\"}\n"),
	})
	got := describeTokenErr(err)
	for _, want := range []string{"HTTP 400", `error="invalid_grant"`, "refresh token reuse", `{"error":"invalid_grant"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing %q", got, want)
		}
	}
	if describeTokenErr(errors.New("dial timeout")) != "dial timeout" {
		t.Error("non-OAuth errors pass through")
	}
}

func TestSharedTokenSource(t *testing.T) {
	m := NewManager(nil, ManagerConfig{}, nil)
	built := 0
	build := func() oauth2.TokenSource {
		built++
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "a"})
	}
	k := key(1, "user", "tl")
	a, b := m.sharedTokenSource(k, build), m.sharedTokenSource(k, build)
	if built != 1 || a != b {
		t.Fatalf("built %d times; concurrent connections must share one source", built)
	}
	m.invalidate("user", "tl") // re-auth / registration change
	m.sharedTokenSource(k, build)
	m.drop(k) // lost sign-in
	m.sharedTokenSource(k, build)
	if built != 3 {
		t.Fatalf("source should be rebuilt after invalidate and drop, built %d", built)
	}
}

func TestWantsOfflineAccess(t *testing.T) {
	rt := []string{"authorization_code", "refresh_token"}
	for _, c := range []struct {
		scopes, grants []string
		want           bool
	}{
		{nil, rt, true},
		{[]string{"offline_access", "x"}, nil, true},
		{[]string{"read"}, rt, false},
		{nil, []string{"authorization_code"}, false},
	} {
		if got := wantsOfflineAccess(c.scopes, c.grants); got != c.want {
			t.Errorf("%v %v: got %v", c.scopes, c.grants, got)
		}
	}
}
