package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/webfetch"
)

type fakeProvider struct{ text string }

func (f fakeProvider) Name() string { return "fake" }
func (f fakeProvider) Chat(ctx context.Context, req llm.ChatRequest) (<-chan llm.Chunk, error) {
	ch := make(chan llm.Chunk, 2)
	ch <- llm.Chunk{TextDelta: f.text}
	ch <- llm.Chunk{Done: true, Model: "fake-model"}
	close(ch)
	return ch, nil
}

func TestDelegatedLLMCallIsLogged(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{
		Provider:      fakeProvider{text: "ANALYSIS RESULT"},
		Session:       sessionlog.New(dir, true, false, nil),
		Temperature:   0.2,
		WebFetchModel: "small-model",
	}
	rc := &runContext{sessionID: 7, userID: 3, turn: 1, step: 2}
	res := webfetch.Result{Markdown: "page md", FinalURL: "https://example.com/x", Title: "X"}

	out, err := a.analyzeWeb(context.Background(), rc, "Summarize", res, "page md", 0, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if out != "ANALYSIS RESULT" {
		t.Fatalf("got %q", out)
	}

	raw, err := os.ReadFile(sessionlog.TrajectoryPath(dir, 3, 7))
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	for _, want := range []string{
		`"type":"delegated_llm_request"`,
		`"type":"delegated_llm_response"`,
		`"delegate":"web_fetch"`,
		`"duration_ms"`,
		`"model":"small-model"`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("trajectory missing %s\n---\n%s", want, log)
		}
	}
}
