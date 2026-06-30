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

	out, err := a.analyzeWeb(context.Background(), rc, webFetchLabel, "Summarize", res, "page md", 0, map[string]bool{})
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

// toolLooperProvider emits a calculator tool call on every completion that is
// offered tools, and plain text only when tools are withheld — modeling an
// analysis model that never answers until forced.
type toolLooperProvider struct{ finalText string }

func (p toolLooperProvider) Name() string { return "looper" }
func (p toolLooperProvider) Chat(ctx context.Context, req llm.ChatRequest) (<-chan llm.Chunk, error) {
	ch := make(chan llm.Chunk, 2)
	if len(req.Tools) > 0 {
		ch <- llm.Chunk{Done: true, Model: "fake-model", ToolCalls: []llm.ToolCall{{
			ID: "c1", Function: llm.FunctionCall{Name: "noop", Arguments: `{}`},
		}}}
	} else {
		ch <- llm.Chunk{TextDelta: p.finalText}
		ch <- llm.Chunk{Done: true, Model: "fake-model"}
	}
	close(ch)
	return ch, nil
}

// When the analysis model exhausts its step budget while only ever calling
// tools, analyzeWeb must still return a non-empty answer (via a final tool-less
// completion) rather than handing back "".
func TestAnalyzeWebNeverReturnsEmptyOnToolLoop(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{
		Provider:      toolLooperProvider{finalText: "FORCED ANSWER"},
		Session:       sessionlog.New(dir, true, false, nil),
		WebFetchModel: "small-model",
		WebFetchTools: true, // exercise the tool-calling loop
	}
	rc := &runContext{sessionID: 1, userID: 1, turn: 1}
	res := webfetch.Result{FinalURL: "https://example.com/x", Title: "X"}
	out, err := a.analyzeWeb(context.Background(), rc, webFetchDOMLabel, "Price?", res, "structural view", 0, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if out != "FORCED ANSWER" {
		t.Fatalf("expected forced final answer, got %q", out)
	}
}

// A WebFetchDOM-originated analysis tags its delegated calls distinctly, so the
// client (SSE Source) and trajectory logs can tell the two apart.
func TestDelegatedLLMCallLabeledWebFetchDOM(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{
		Provider:      fakeProvider{text: "ANALYSIS RESULT"},
		Session:       sessionlog.New(dir, true, false, nil),
		WebFetchModel: "small-model",
	}
	rc := &runContext{sessionID: 7, userID: 3, turn: 1, step: 2}
	res := webfetch.Result{FinalURL: "https://example.com/x", Title: "X"}

	if _, err := a.analyzeWeb(context.Background(), rc, webFetchDOMLabel, "Price?", res, "structural view", 0, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sessionlog.TrajectoryPath(dir, 3, 7))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); !strings.Contains(got, `"delegate":"web_fetch_dom"`) {
		t.Errorf("trajectory should tag delegate web_fetch_dom\n---\n%s", got)
	}
}
