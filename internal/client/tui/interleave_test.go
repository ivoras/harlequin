package tui

import (
	"testing"

	clientcfg "github.com/ivoras/harlequin/internal/client/config"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Reasoning, answer text, and tool calls should land in the transcript in the
// order they occurred, not collapse into separate sections.
func TestStreamEventsInterleaveChronologically(t *testing.T) {
	m := &Model{cfg: &clientcfg.Config{ShowThinking: true}, styles: newStyles(), width: 80}

	events := []types.StreamEvent{
		{Type: types.SSEThinking, Thinking: "let me check the page"},
		{Type: types.SSEToken, Text: "Looking it up."},
		{Type: types.SSEToolCall, ToolName: "WebFetch", ToolArgs: `{"url":"x"}`},
		{Type: types.SSEToolResult, Output: "rate=0.85"},
		{Type: types.SSEThinking, Thinking: "now compute"},
		{Type: types.SSEToken, Text: "It costs 8.5 EUR."},
	}
	for _, ev := range events {
		m.handleStreamEvent(ev)
	}
	m.flushStreaming() // what streamEndMsg does at turn end

	var got []string
	for _, b := range m.blocks {
		got = append(got, b.role)
	}
	want := []string{"thinking", "assistant", "tool", "tool", "thinking", "assistant"}
	if len(got) != len(want) {
		t.Fatalf("block roles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("block roles = %v, want %v", got, want)
		}
	}
	// Pre-tool reasoning must come before the tool block; post-tool after it.
	if m.blocks[0].text != "let me check the page" || m.blocks[4].text != "now compute" {
		t.Fatalf("thinking text misplaced: %q / %q", m.blocks[0].text, m.blocks[4].text)
	}
}
