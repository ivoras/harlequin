package llm

import (
	"encoding/json"
	"unicode/utf8"
)

// EstimateMessagesTokens approximates input tokens for a chat request (messages +
// tool definitions). Providers count slightly differently; use API prompt_tokens
// when available.
func EstimateMessagesTokens(msgs []Message, tools []Tool) int {
	n := 3 // reply priming bias
	for _, m := range msgs {
		n += 4
		n += estimateTextTokens(m.Role)
		n += estimateTextTokens(m.Content)
		n += estimateTextTokens(m.Name)
		n += estimateTextTokens(m.ToolCallID)
		for _, tc := range m.ToolCalls {
			n += 4
			n += estimateTextTokens(tc.Function.Name)
			n += estimateTextTokens(tc.Function.Arguments)
		}
	}
	for _, t := range tools {
		n += 4
		n += estimateTextTokens(t.Function.Name)
		n += estimateTextTokens(t.Function.Description)
		if t.Function.Parameters != nil {
			if b, err := json.Marshal(t.Function.Parameters); err == nil {
				n += estimateTextTokens(string(b))
			}
		}
	}
	return n
}

func estimateTextTokens(s string) int {
	if s == "" {
		return 0
	}
	// Rough OpenAI-style heuristic: ~4 characters per token.
	n := utf8.RuneCountInString(s)
	return (n + 3) / 4
}
