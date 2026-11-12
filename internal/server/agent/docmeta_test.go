package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/llm"
)

type oneShotProvider struct{ req *llm.ChatRequest }

func (oneShotProvider) Name() string { return "one" }
func (p oneShotProvider) Chat(_ context.Context, req llm.ChatRequest) (<-chan llm.Chunk, error) {
	*p.req = req
	ch := make(chan llm.Chunk, 2)
	ch <- llm.Chunk{TextDelta: "Ugovor o najmu"}
	ch <- llm.Chunk{Done: true, Model: "fake"}
	close(ch)
	return ch, nil
}

func TestLanguageName(t *testing.T) {
	for in, want := range map[string]string{"": "English", "en": "English", "hr": "Croatian", " de ": "German", "zz-nonsense-x": "zz-nonsense-x"} {
		if got := languageName(in); got != want {
			t.Errorf("languageName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Descriptions are requested in the organisation's language.
func TestDescribeUsesOrgLanguage(t *testing.T) {
	var req llm.ChatRequest
	a := &Agent{Provider: oneShotProvider{&req}, OrgLanguage: "hr"}
	if got := a.describeOnce(context.Background(), "Lease", "This lease agreement...", 5*time.Second); got != "Ugovor o najmu" {
		t.Fatalf("description %q", got)
	}
	if len(req.Messages) != 2 || !strings.HasSuffix(req.Messages[1].Content, "Write the description in Croatian.") {
		t.Fatalf("prompt: %+v", req.Messages)
	}
}
