package tui

import (
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestFormatContextBreakdownCost(t *testing.T) {
	out := formatContextBreakdown(&types.ContextBreakdown{Total: 10, Cost: &types.SessionCost{
		USD: 0.0302, Calls: 3, ReportedCalls: 2, PromptTokens: 1000, CachedTokens: 500, CompletionTokens: 20}})
	for _, want := range []string{"Session cost: $0.030 over 3 LLM call(s)", "(50% cached)", "1 call(s) estimated"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
