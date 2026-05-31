package agent

import (
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestRenderResultsIncludesIDAndSlotKey(t *testing.T) {
	t.Parallel()
	out := renderResults([]types.SearchResult{{
		ID: "s.4", SlotKey: "organisation.name",
		Content: "The organisation name is WoodChucks Inc.",
	}})
	if !strings.Contains(out, "[s.4 {organisation.name}]") {
		t.Fatalf("got %q", out)
	}
}
