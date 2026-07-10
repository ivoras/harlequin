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

func TestQualifiedProjectRefs(t *testing.T) {
	t.Parallel()
	// parseDocRefs: p<id>.N resolves to that project's qualified scope.
	byScope, errMsg := parseDocRefs([]string{"p3.17", "p.4", "u.2", "d.p12.9"})
	if errMsg != "" {
		t.Fatalf("unexpected error: %s", errMsg)
	}
	if got := byScope["project:3"]; len(got) != 1 || got[0] != 17 {
		t.Fatalf("project:3 docs = %v, want [17]", got)
	}
	if got := byScope["project:12"]; len(got) != 1 || got[0] != 9 {
		t.Fatalf("project:12 docs = %v, want [9]", got)
	}
	if got := byScope["project"]; len(got) != 1 || got[0] != 4 {
		t.Fatalf("project docs = %v, want [4]", got)
	}
	if _, errMsg = parseDocRefs([]string{"px.1"}); errMsg == "" {
		t.Fatal("px.1 should be rejected")
	}
	// scopeLetter round-trips the qualified scope back to the p<id> prefix.
	if got := scopeLetter("project:3"); got != "p3" {
		t.Fatalf("scopeLetter(project:3) = %q, want p3", got)
	}
	if got := scopeLetter("project"); got != "p" {
		t.Fatalf("scopeLetter(project) = %q, want p", got)
	}
}
