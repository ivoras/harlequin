package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/webfetch"
)

// prompt without a grep/selector is rejected before any fetch, so the analysis
// model is never handed the whole structural dump it can't answer from. The
// error must arrive before the loop guard marks the URL seen, so a corrected
// retry on the same URL still works.
func TestWebFetchDOMPromptRequiresQueryMode(t *testing.T) {
	a := &Agent{WebFetcher: &webfetch.Client{}} // fetch is never reached
	rc := &runContext{}
	seen := map[string]bool{}

	out, err := a.webFetchDOM(context.Background(), rc, map[string]any{
		"url":    "https://example.com/x",
		"prompt": "What is the price?",
	}, 0, seen)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "prompt requires a query mode") {
		t.Fatalf("expected query-mode error, got %q", out)
	}
	if seen["https://example.com/x"] {
		t.Error("URL must not be marked seen when the call is rejected, or the corrected retry would be blocked")
	}
}
