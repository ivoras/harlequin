package tui

import (
	"strings"
	"testing"

	clientcfg "github.com/ivoras/harlequin/internal/client/config"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestFormatExportBlock_roles(t *testing.T) {
	t.Parallel()
	if got := formatExportBlock(roleBlock{"user", "hello"}); !strings.Contains(got, "## You") || !strings.Contains(got, "hello") {
		t.Fatalf("user: %q", got)
	}
	if got := formatExportBlock(roleBlock{"thinking", "reasoning\nstep"}); !strings.Contains(got, "### Thinking") || !strings.Contains(got, "> reasoning") {
		t.Fatalf("thinking: %q", got)
	}
	if got := formatExportBlock(roleBlock{"tool", "⚙ memory_search(q)\n  ↳ hits"}); !strings.Contains(got, "## Tool") || !strings.Contains(got, "memory_search") {
		t.Fatalf("tool: %q", got)
	}
}

func TestFormatSessionMarkdown_header(t *testing.T) {
	t.Parallel()
	m := &Model{
		cfg:       &clientcfg.Config{ServerURL: "http://localhost:8080"},
		user:      &types.User{Email: "alice"},
		sessionID: 7,
		blocks:    []roleBlock{{role: "user", text: "hi"}},
	}
	out := formatSessionMarkdown(m, m.blocks)
	if !strings.Contains(out, "**User:** alice") || !strings.Contains(out, "**Session ID:** 7") {
		t.Fatalf("header missing fields:\n%s", out)
	}
}

func TestOnlySessionFiltersToUserAndAssistant(t *testing.T) {
	blocks := []roleBlock{
		{role: "user", text: "hi"},
		{role: "thinking", text: "hmm"},
		{role: "tool", text: "⚙ WebFetch(...)"},
		{role: "assistant", text: "hello"},
		{role: "status", text: "connected"},
		{role: "error", text: "boom"},
	}
	got := onlySession(blocks)
	if len(got) != 2 || got[0].role != "user" || got[1].role != "assistant" {
		t.Fatalf("got %+v", got)
	}
	// raw keeps everything; default keeps only the session.
	if len(onlySession(blocks)) == len(blocks) {
		t.Fatal("non-raw export should drop non-session blocks")
	}
}
