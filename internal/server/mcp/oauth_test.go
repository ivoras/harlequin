package mcp

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
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
