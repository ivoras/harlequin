package agent

import (
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestRenderResultsIncludesIDAndSlotKey(t *testing.T) {
	t.Parallel()
	out := renderResults([]types.SearchResult{{
		ID: "s.4", SlotKeys: []string{"organisation.name"},
		Content: "The organisation name is WoodChucks Inc.",
	}})
	if !strings.Contains(out, "[s.4 {organisation.name}]") {
		t.Fatalf("got %q", out)
	}
}

func TestRenderResultsJoinsMultipleSlotKeys(t *testing.T) {
	t.Parallel()
	out := renderResults([]types.SearchResult{{
		ID: "u.16", SlotKeys: []string{"memory.date", "user.birthday"},
		Content: "December 5th",
	}})
	if !strings.Contains(out, "[u.16 {memory.date, user.birthday}]") {
		t.Fatalf("got %q", out)
	}
}
