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
		cfg:            &clientcfg.Config{ServerURL: "http://localhost:8080"},
		user:           &types.User{Username: "alice"},
		conversationID: 7,
		blocks:         []roleBlock{{role: "user", text: "hi"}},
	}
	out := formatSessionMarkdown(m, m.blocks)
	if !strings.Contains(out, "**User:** alice") || !strings.Contains(out, "**Conversation ID:** 7") {
		t.Fatalf("header missing fields:\n%s", out)
	}
}
