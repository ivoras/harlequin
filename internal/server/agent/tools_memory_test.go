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

func TestRenderDocResultsHeaderAndSource(t *testing.T) {
	t.Parallel()
	out := renderDocResults([]types.SearchResult{{
		ID: "d.u.943", Scope: "personal", Source: "teu_tfeu.pdf · chunk 942",
		Content: "The Council shall have its seat in Brussels.",
	}})
	if !strings.Contains(out, "Ranked document results") {
		t.Fatalf("missing doc header: %q", out)
	}
	if strings.Contains(out, "memory_change") {
		t.Fatalf("doc output mentions memory tools: %q", out)
	}
	if !strings.Contains(out, "[d.u.943 personal · teu_tfeu.pdf · chunk 942]") {
		t.Fatalf("got %q", out)
	}
}
