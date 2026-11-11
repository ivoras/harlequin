package agent

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/session"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// history builds user, then n (assistant tool call, tool result of size chars) pairs.
func history(n, size int) []llm.Message {
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: "sys"}, {Role: llm.RoleUser, Content: "go"}}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs,
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: id, Name: "t", Content: strings.Repeat("x", size)})
	}
	return msgs
}

func TestElideOldToolResults(t *testing.T) {
	msgs := history(10, 8000) // ~20k tokens
	if out, st := elideOldToolResults(msgs, nil, 1_000_000, 3); st.Elided != 0 || &out[0] != &msgs[0] {
		t.Fatalf("under threshold must not change anything: %+v", st)
	}
	out, st := elideOldToolResults(msgs, nil, 12000, 3)
	if st.Elided == 0 || st.TokensAfter > 12000*6/10 || st.TokensBefore <= 12000 {
		t.Fatalf("stats %+v", st)
	}
	if strings.HasPrefix(msgs[3].Content, elidedPrefix) {
		t.Fatal("input slice was modified")
	}
	// Oldest results are elided first, the newest keep=3 stay whole, and every
	// tool message keeps its call id (pairing stays valid).
	tools := 0
	for i, m := range out {
		if m.Role != llm.RoleTool {
			continue
		}
		tools++
		if m.ToolCallID != msgs[i].ToolCallID || m.Name != "t" {
			t.Fatalf("tool message %d lost its pairing", i)
		}
	}
	last := out[len(out)-1]
	if tools != 10 || strings.HasPrefix(last.Content, elidedPrefix) || !strings.HasPrefix(out[3].Content, elidedPrefix) {
		t.Fatalf("tools=%d first=%q last-elided=%v", tools, out[3].Content, strings.HasPrefix(last.Content, elidedPrefix))
	}
	for _, m := range out[len(out)-6:] { // the newest 3 results (and their calls)
		if m.Role == llm.RoleTool && strings.HasPrefix(m.Content, elidedPrefix) {
			t.Fatal("a protected result was elided")
		}
	}
	// A second pass over the compacted history changes nothing (and costs no tokens).
	if _, st2 := elideOldToolResults(out, nil, 12000, 3); st2.Elided != 0 {
		t.Fatalf("second pass elided %d more", st2.Elided)
	}
}

func TestElideLeavesSmallResultsAndDisabled(t *testing.T) {
	small := history(10, 100)
	if _, st := elideOldToolResults(small, nil, 1, 0); st.Elided != 0 {
		t.Fatal("results under the minimum size must stay")
	}
	if _, st := elideOldToolResults(history(10, 8000), nil, 0, 0); st.Elided != 0 {
		t.Fatal("threshold 0 disables compaction")
	}
}

func TestCompactionConfigDefaultsAndWindowCap(t *testing.T) {
	var c config.CompactionConfig
	if c.Threshold() != 150000 || c.Keep() != 8 {
		t.Fatalf("defaults %d/%d", c.Threshold(), c.Keep())
	}
	off := false
	if (config.CompactionConfig{Enabled: &off}).Threshold() != 0 {
		t.Fatal("enabled:false must give 0")
	}
	a := &Agent{CompactThreshold: 150000}
	if a.compactThreshold(0) != 150000 || a.compactThreshold(1048576) != 150000 || a.compactThreshold(100000) != 60000 {
		t.Fatalf("cap: %d %d %d", a.compactThreshold(0), a.compactThreshold(1048576), a.compactThreshold(100000))
	}
}

// recordingProvider calls run_js (big output) for `calls` steps, then answers,
// recording each request's messages.
type recordingProvider struct {
	calls int
	reqs  *[][]llm.Message
}

func (recordingProvider) Name() string { return "rec" }
func (p recordingProvider) Chat(_ context.Context, req llm.ChatRequest) (<-chan llm.Chunk, error) {
	*p.reqs = append(*p.reqs, append([]llm.Message(nil), req.Messages...))
	ch := make(chan llm.Chunk, 2)
	if len(*p.reqs) <= p.calls {
		ch <- llm.Chunk{Done: true, Model: "fake", ToolCalls: []llm.ToolCall{{
			ID: fmt.Sprintf("c%d", len(*p.reqs)), Function: llm.FunctionCall{Name: "run_js", Arguments: `{"code":"println('y'.repeat(6000))"}`},
		}}}
	} else {
		ch <- llm.Chunk{TextDelta: "done"}
		ch <- llm.Chunk{Done: true, Model: "fake"}
	}
	close(ch)
	return ch, nil
}

// Inside the real step loop, once the prompt passes the threshold, old tool
// results are stubbed in the requests the provider sees, the request size stays
// bounded, and the stored session keeps the full results.
func TestRunCompactsOldToolResultsInTheStepLoop(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	sessions := session.NewStore()
	var reqs [][]llm.Message
	a := &Agent{
		Provider: recordingProvider{calls: 12, reqs: &reqs}, Storage: mgr, Sessions: sessions, MaxSteps: 20,
		Runner:           jsrun.New(jsrun.Options{}),
		Skills:           skills.NewManager(mgr.Shared, fstest.MapFS{}, jsrun.New(jsrun.Options{}), nil),
		CompactThreshold: 12000, CompactKeep: 2,
	}
	var sessID int64
	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		s, err := sessions.Create(ctx, udb, 1, "t", "", types.APIREST, types.InterfaceWeb)
		sessID = s.ID
		return err
	})
	if err := a.Run(ctx, 0, sessID, 1, "u@x", types.RoleUser, "", "", "go", func(types.StreamEvent) {}); err != nil {
		t.Fatal(err)
	}
	last := reqs[len(reqs)-1]
	elided, whole := 0, 0
	for _, m := range last {
		if m.Role == llm.RoleTool {
			if strings.HasPrefix(m.Content, elidedPrefix) {
				elided++
			} else {
				whole++
			}
		}
	}
	size := llm.EstimateMessagesTokens(last, nil)
	if elided == 0 || whole < 2 || size > 12000*2 {
		t.Fatalf("elided=%d whole=%d tokens=%d (threshold 12000)", elided, whole, size)
	}
	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		msgs, _ := sessions.Messages(ctx, udb, sessID)
		for _, m := range msgs {
			if m.Role == llm.RoleTool && strings.HasPrefix(m.Content, elidedPrefix) {
				t.Error("the stored session must keep full tool results")
			}
		}
		return nil
	})
}
