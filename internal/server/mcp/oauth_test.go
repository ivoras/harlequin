package mcp

import (
	"reflect"
	"testing"
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
