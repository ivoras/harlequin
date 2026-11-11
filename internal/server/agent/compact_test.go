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

func stubbed(m llm.Message) bool { return strings.HasPrefix(m.Content, elidedPrefix) }

func TestCompactHistory(t *testing.T) {
	msgs := history(10, 8000) // ~20k tokens
	if out, st := compactHistory(msgs, nil, 1_000_000, 3); st.Total != 0 || &out[0] != &msgs[0] {
		t.Fatalf("under threshold must not change anything: %+v", st)
	}
	out, st := compactHistory(msgs, nil, 12000, 3)
	if st.Total == 0 || st.TokensAfter > 12000 {
		t.Fatalf("stats %+v", st)
	}
	if stubbed(msgs[3]) {
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
	if tools != 10 || !stubbed(out[3]) {
		t.Fatalf("tools=%d first=%q", tools, out[3].Content)
	}
	for _, m := range out[len(out)-6:] { // the newest 3 results (and their calls)
		if stubbed(m) {
			t.Fatal("a protected result was elided")
		}
	}
	// A second pass over the compacted history changes nothing.
	if _, st2 := compactHistory(out, nil, 12000, 3); st2.Total != 0 {
		t.Fatalf("second pass elided %d more", st2.Total)
	}
}

// The replay is what keeps the prompt prefix cacheable: compacting a longer
// history (a later step, or a later turn that reloaded the full results) makes
// the same decisions for the part already sent, and adds stubs only now and
// then (hysteresis), not at every request.
func TestCompactHistoryIsStableAsTheConversationGrows(t *testing.T) {
	msgs := history(60, 8000) // 60 steps, ~2k tokens each
	var prev []llm.Message
	requests, changed := 0, 0
	for end := 2; end <= len(msgs); end += 2 { // each request: history up to a tool result
		out, st := compactHistory(msgs[:end], nil, 12000, 3)
		requests++
		rewrote := false
		for i, m := range prev {
			if m.Content != out[i].Content {
				if stubbed(m) || !stubbed(out[i]) {
					t.Fatalf("request ending at %d rewrote message %d other than by stubbing it", end, i)
				}
				rewrote = true
			}
		}
		if rewrote != (st.Elided > 0) {
			t.Fatalf("request ending at %d: prefix changed=%v but stats say %+v", end, rewrote, st)
		}
		if rewrote {
			changed++
		}
		if size := llm.EstimateMessagesTokens(out, nil); size > 12000+2100 {
			t.Fatalf("request ending at %d is ~%d tokens (threshold 12000)", end, size)
		}
		prev = out
	}
	// ~2k tokens/step against 4.8k of slack: a new cut-off every 2-3 steps.
	if changed == 0 || changed > requests/2 {
		t.Fatalf("prefix changed in %d of %d requests", changed, requests)
	}
}

// Results of the step that just ran are never stubbed, however many there are:
// the model has not read them yet.
func TestCompactHistoryKeepsUnreadResults(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: "sys"}, {Role: llm.RoleUser, Content: "go"}}
	step := func() {
		var calls []llm.ToolCall
		for i := 0; i < 12; i++ {
			calls = append(calls, llm.ToolCall{ID: fmt.Sprintf("c%d-%d", len(msgs), i)})
		}
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: calls})
		for _, c := range calls {
			msgs = append(msgs, llm.Message{Role: llm.RoleTool, ToolCallID: c.ID, Name: "t", Content: strings.Repeat("x", 8000)})
		}
	}
	step() // 12 parallel results, ~24k tokens, keep is only 2
	if _, st := compactHistory(msgs, nil, 12000, 2); st.Total != 0 {
		t.Fatalf("stubbed %d results the model never saw", st.Total)
	}
	first := len(msgs)
	step()
	out, st := compactHistory(msgs, nil, 12000, 2)
	if st.Total != 12 {
		t.Fatalf("the first step's results should now be stubbed: %+v", st)
	}
	for _, m := range out[first:] {
		if stubbed(m) {
			t.Fatal("stubbed a result of the step that just ran")
		}
	}
}

func TestCompactLeavesSmallResultsAndDisabled(t *testing.T) {
	if _, st := compactHistory(history(10, 100), nil, 1, 0); st.Total != 0 {
		t.Fatal("results under the minimum size must stay")
	}
	if _, st := compactHistory(history(10, 8000), nil, 0, 0); st.Total != 0 {
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
		CompactThreshold: 150000, CompactKeep: 2,
		// The window (so the 60% cap: 12000) is known before the first call.
		ContextMax: func(model string) int {
			if model != "" && model != "fake" {
				t.Errorf("ContextMax(%q)", model)
			}
			return 20000
		},
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
	turn1 := len(reqs)
	// A second turn reloads the full results from the store and must stub them
	// again exactly as the first turn did.
	if err := a.Run(ctx, 0, sessID, 1, "u@x", types.RoleUser, "", "", "more", func(types.StreamEvent) {}); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != turn1+1 {
		t.Fatalf("second turn made %d requests", len(reqs)-turn1)
	}
	for r := 1; r < len(reqs); r++ {
		for i, m := range reqs[r-1] {
			if i == 0 || m.Content == stepLimitNudge {
				continue // system prompt; last-step nudge
			}
			got := reqs[r][i]
			if strings.HasPrefix(m.Content, got.Content) && !stubbed(m) {
				continue // the step-budget note is this turn's context only
			}
			if got.Content != m.Content && (stubbed(m) || !stubbed(got)) {
				t.Fatalf("request %d rewrote message %d of the previous request", r, i)
			}
		}
	}
	if n := len(reqs[turn1]); stubbed(reqs[turn1][n-1]) || reqs[turn1][n-1].Content != "more" {
		t.Fatalf("second turn's request ends with %q", reqs[turn1][n-1].Content)
	}
	last := reqs[turn1-1]
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
