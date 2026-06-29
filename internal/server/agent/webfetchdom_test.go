package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/webfetch"
)

func TestDeriveGrepFromPrompt(t *testing.T) {
	cases := map[string]string{
		"What's the price of this watch?": "price|watch",
		"How much does it cost?":          "much|cost",
		"Is it in stock?":                 "stock",
		"What is the model number?":       "model|number",
		// All stopwords / too-short tokens → nothing usable.
		"what is it":  "",
		"how are you": "",
	}
	for prompt, want := range cases {
		if got := deriveGrepFromPrompt(prompt); got != want {
			t.Errorf("deriveGrepFromPrompt(%q) = %q, want %q", prompt, got, want)
		}
	}
}

// A prompt with no derivable keywords still errors (and must not mark the URL
// seen, so the model's corrected retry on the same URL isn't blocked).
func TestWebFetchDOMPromptWithNoKeywordsErrors(t *testing.T) {
	a := &Agent{WebFetcher: &webfetch.Client{}} // fetch is never reached
	rc := &runContext{}
	seen := map[string]bool{}

	out, err := a.webFetchDOM(context.Background(), rc, map[string]any{
		"url":    "https://example.com/x",
		"prompt": "what is it", // all stopwords
	}, 0, seen)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "none could be derived") {
		t.Fatalf("expected no-keyword error, got %q", out)
	}
	if seen["https://example.com/x"] {
		t.Error("URL must not be marked seen when the call is rejected")
	}
}
