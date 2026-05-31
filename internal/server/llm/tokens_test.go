package llm

import "testing"

func TestEstimateMessagesTokens(t *testing.T) {
	t.Parallel()
	msgs := []Message{
		{Role: RoleSystem, Content: "You are helpful."},
		{Role: RoleUser, Content: "Hello"},
	}
	n := EstimateMessagesTokens(msgs, nil)
	if n < 5 {
		t.Fatalf("estimate too small: %d", n)
	}
}

func TestContextMax(t *testing.T) {
	t.Parallel()
	if got := ContextMax("openai/gpt-4o-mini", nil); got != 128_000 {
		t.Fatalf("got %d", got)
	}
	if got := ContextMax("custom", map[string]int{"custom": 4096}); got != 4096 {
		t.Fatalf("override got %d", got)
	}
}
