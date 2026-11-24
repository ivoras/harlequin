package agent

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/session"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestRepeatGuard(t *testing.T) {
	g := repeatGuard{}
	if note, stop := g.check("run_js", `{"code":"x"}`, "out"); note != "" || stop != "" {
		t.Fatalf("first call flagged: %q %v", note, stop)
	}
	// Same call, keys reordered and spaced differently: still a repeat.
	note, stop := g.check("run_js", `{ "code": "x" }`, "out")
	if !strings.Contains(note, "exact run_js call") || stop != "" {
		t.Fatalf("second call: %q %v", note, stop)
	}
	// Same args, different result (state changed): not a repeat.
	if note, _ := g.check("run_js", `{"code":"x"}`, "other"); note != "" {
		t.Fatalf("different result flagged: %q", note)
	}
	if _, stop := g.check("run_js", `{"code":"x"}`, "out"); stop != "" {
		t.Fatal("stopped at 3")
	}
	if _, stop := g.check("run_js", `{"code":"x"}`, "out"); stop == "" {
		t.Fatalf("not stopped at %d", maxIdenticalCalls)
	}
	if note, _ := g.check("read_doc", `{"code":"x"}`, "out"); note != "" {
		t.Fatal("other tool flagged")
	}
}

// The loop seen in production: run_js printing slices of one text with the
// offsets shifted by one each call. No two calls are identical, but every
// output is text the model already has.
func TestRepeatGuardStaleSlices(t *testing.T) {
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("%d Component %d   some description of part %d   1   %d,99", i, i, i, i*10))
	}
	text := strings.Join(lines, "\n")
	g := repeatGuard{}
	if note, stop := g.check("run_js", `{"code":"print(all)"}`, text); note != "" || stop != "" {
		t.Fatalf("first read flagged: %q %q", note, stop)
	}
	stoppedAt := 0
	for i := 0; i < 20 && stoppedAt == 0; i++ {
		a, b := 300-i, 700-i
		out := text[:a] + "\n====\n" + text[a:b]
		if _, stop := g.check("run_js", fmt.Sprintf(`{"code":"slice(0,%d)"}`, a), out); stop != "" {
			stoppedAt = i + 1
		}
	}
	if stoppedAt != maxStaleCalls {
		t.Fatalf("stopped after %d slice calls, want %d", stoppedAt, maxStaleCalls)
	}

	// Paging through new text is progress, and resets the count.
	g = repeatGuard{}
	for i := 0; i < 30; i++ {
		page := strings.Repeat(fmt.Sprintf("page %d line with fresh content\n", i), 10)
		if _, stop := g.check("read_doc", fmt.Sprintf(`{"offset":%d}`, i), page); stop != "" {
			t.Fatalf("paging stopped at %d", i)
		}
	}
	// Re-reads interleaved with new material never stop the turn.
	for i := 0; i < 30; i++ {
		if _, stop := g.check("read_doc", fmt.Sprintf(`{"offset":0,"try":%d}`, i), strings.Repeat("page 0 line with fresh content\n", 10)); stop != "" {
			t.Fatalf("re-read stopped at %d", i)
		}
		g.check("search_docs", fmt.Sprintf(`{"q":%d}`, i), strings.Repeat(fmt.Sprintf("new hit %d about something\n", i), 10))
	}
}

// A model resending one call verbatim is stopped after maxIdenticalCalls with
// a stored error, instead of burning the whole step budget.
func TestRepeatedToolCallStopsTurn(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	sessions := session.NewStore()
	calls := 0
	a := &Agent{
		Provider: countingProvider{toolLooperProvider{}, &calls}, Storage: mgr, Sessions: sessions, MaxSteps: 20,
		Skills: skills.NewManager(mgr.Shared, fstest.MapFS{}, jsrun.New(jsrun.Options{}), nil),
	}
	var sessID int64
	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		s, err := sessions.Create(ctx, udb, 1, "t", "", types.APIREST, types.InterfaceWeb)
		sessID = s.ID
		return err
	})
	err = a.Run(ctx, 0, sessID, 1, "u@x", types.RoleUser, "", "", "question?", func(types.StreamEvent) {})
	if err == nil || !strings.Contains(err.Error(), "kept repeating the same noop call") {
		t.Fatalf("err = %v", err)
	}
	if calls != maxIdenticalCalls {
		t.Fatalf("%d LLM calls, want %d", calls, maxIdenticalCalls)
	}
}

// A looped turn's tool calls are hidden from the next turn, replaced by a note;
// other turns and other stored errors are untouched.
func TestModelHistoryHidesLoopedTurn(t *testing.T) {
	call := []types.ToolCall{{ID: "c1", Name: "run_js", Arguments: "{}"}}
	h := []types.Message{
		{Role: llm.RoleUser, Content: "first"},
		{Role: llm.RoleAssistant, Content: "ok"},
		{Role: llm.RoleError, Content: "provider down"},
		{Role: llm.RoleUser, Content: "compare"},
		{Role: llm.RoleAssistant, ToolCalls: call},
		{Role: llm.RoleTool, Content: "0", ToolCallID: "c1", Name: "run_js"},
		{Role: llm.RoleAssistant, ToolCalls: call},
		{Role: llm.RoleTool, Content: "0", ToolCallID: "c1", Name: "run_js"},
		{Role: llm.RoleError, Content: repeatStopPrefix + "repeating the same run_js call"},
		{Role: llm.RoleUser, Content: "try again"},
	}
	var got []string
	for _, m := range modelHistory(h) {
		got = append(got, m.Role+":"+m.Content)
	}
	want := []string{"user:first", "assistant:ok", "user:compare", "assistant:" + repeatStoppedNote, "user:try again"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

type countingProvider struct {
	llm.Provider
	n *int
}

func (p countingProvider) Chat(ctx context.Context, req llm.ChatRequest) (<-chan llm.Chunk, error) {
	*p.n++
	return p.Provider.Chat(ctx, req)
}
