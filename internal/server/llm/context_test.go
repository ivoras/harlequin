package llm

import "testing"

func TestContextMaxOverrideMatching(t *testing.T) {
	overrides := map[string]int{"deepseek/deepseek-v4-flash": 1_048_576}
	cases := []struct {
		model string
		want  int
	}{
		{"deepseek/deepseek-v4-flash", 1_048_576},          // exact
		{"deepseek-v4-flash", 1_048_576},                   // vendor prefix missing
		{"deepseek/deepseek-v4-flash-20260423", 1_048_576}, // dated canonical slug (OpenRouter)
		{"deepseek-v4-flash-20260423", 1_048_576},          // dated, prefixless
		{"deepseek-v4-flashy", 8192},                       // no dash boundary -> no match
		{"unrelated-model", 8192},                          // no match -> default
	}
	for _, c := range cases {
		if got := ContextMax(c.model, overrides); got != c.want {
			t.Errorf("ContextMax(%q) = %d, want %d", c.model, got, c.want)
		}
	}
}
