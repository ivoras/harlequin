package agent

import (
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestLanguageHintListsFirstThreeUserMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: llm.RoleUser, Content: "Sažmi mi ovaj ugovor"},
		{Role: llm.RoleAssistant, Content: "This lease agreement is made between..."},
		{Role: llm.RoleUser, Content: "  "},
		{Role: llm.RoleUser, Content: "a"}, {Role: llm.RoleUser, Content: "b"}, {Role: llm.RoleUser, Content: "c"},
	}
	got := languageHint(msgs)
	if !strings.Contains(got, "- Sažmi mi ovaj ugovor\n- a\n- b\n") || strings.Contains(got, "- c") || strings.Contains(got, "lease") {
		t.Fatalf("hint = %q", got)
	}
	if languageHint([]types.Message{{Role: llm.RoleAssistant, Content: "x"}}) != "" {
		t.Fatal("no user messages must give no hint")
	}
}
