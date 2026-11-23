package agent

import (
	"context"
	"database/sql"
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
	if note, stop := g.check("run_js", `{"code":"x"}`, "out"); note != "" || stop {
		t.Fatalf("first call flagged: %q %v", note, stop)
	}
	// Same call, keys reordered and spaced differently: still a repeat.
	note, stop := g.check("run_js", `{ "code": "x" }`, "out")
	if !strings.Contains(note, "exact run_js call") || stop {
		t.Fatalf("second call: %q %v", note, stop)
	}
	// Same args, different result (state changed): not a repeat.
	if note, _ := g.check("run_js", `{"code":"x"}`, "other"); note != "" {
		t.Fatalf("different result flagged: %q", note)
	}
	if _, stop := g.check("run_js", `{"code":"x"}`, "out"); stop {
		t.Fatal("stopped at 3")
	}
	if _, stop := g.check("run_js", `{"code":"x"}`, "out"); !stop {
		t.Fatalf("not stopped at %d", maxIdenticalCalls)
	}
	if note, _ := g.check("read_doc", `{"code":"x"}`, "out"); note != "" {
		t.Fatal("other tool flagged")
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

type countingProvider struct {
	llm.Provider
	n *int
}

func (p countingProvider) Chat(ctx context.Context, req llm.ChatRequest) (<-chan llm.Chunk, error) {
	*p.n++
	return p.Provider.Chat(ctx, req)
}
