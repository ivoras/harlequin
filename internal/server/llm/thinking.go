package llm

// normalizeThinking extracts reasoning/thinking text from a provider stream delta,
// mapping provider-specific field names to a single "thinking" concept.
//
// Known sources (OpenAI-compatible extensions):
//   - reasoning_content (DeepSeek-R1, many OpenRouter reasoning models)
//   - reasoning
//   - thinking
//   - thought
func normalizeThinking(fields map[string]any) string {
	if fields == nil {
		return ""
	}
	for _, key := range []string{
		"reasoning_content",
		"reasoning",
		"thinking",
		"thought",
	} {
		if v, ok := fields[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
