package llm

import "strings"

// defaultContextWindows holds well-known model context limits (tokens). Config
// overrides take precedence.
var defaultContextWindows = map[string]int{
	"openai/gpt-4o":           128_000,
	"openai/gpt-4o-mini":      128_000,
	"openai/gpt-4.1":          1_047_576,
	"openai/gpt-4.1-mini":     1_047_576,
	"openai/gpt-4.1-nano":     1_047_576,
	"anthropic/claude-3.5-sonnet": 200_000,
	"anthropic/claude-3.7-sonnet": 200_000,
	"anthropic/claude-sonnet-4":   200_000,
	"google/gemini-2.0-flash":     1_048_576,
	"google/gemini-2.5-pro":         1_048_576,
}

const defaultContextWindow = 8192

// ContextMax returns the configured or known context window for model. Zero
// means unknown (callers may treat as defaultContextWindow).
func ContextMax(model string, overrides map[string]int) int {
	if model == "" {
		return 0
	}
	if overrides != nil {
		if n, ok := overrides[model]; ok && n > 0 {
			return n
		}
	}
	if n, ok := defaultContextWindows[model]; ok {
		return n
	}
	// OpenRouter-style ids: try the segment after the last slash.
	if i := strings.LastIndex(model, "/"); i >= 0 {
		if n, ok := defaultContextWindows[model[i+1:]]; ok {
			return n
		}
	}
	return defaultContextWindow
}
