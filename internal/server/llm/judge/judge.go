// Package judge defines the server-wide convention for autonomous LLM judgments.
//
// Any background or automatic process (not directly started by the user) that asks
// the LLM to decide whether to act — extract a memory, classify content, gate an
// side-effect, etc. — must follow the confidence contract documented in AGENTS.md.
package judge

import (
	"encoding/json"
	"strings"
)

const (
	// MinConfidence is the minimum score (inclusive) for acting on an autonomous judgment.
	MinConfidence = 7
	// MaxConfidence is the top of the confidence scale requested from the model.
	MaxConfidence = 10
)

// Accept reports whether an autonomous process should act on a judgment score.
func Accept(confidence int) bool {
	return Clamp(confidence) >= MinConfidence
}

// Clamp normalizes a model-supplied score to the 1–10 scale.
func Clamp(confidence int) int {
	if confidence < 1 {
		return 1
	}
	if confidence > MaxConfidence {
		return MaxConfidence
	}
	return confidence
}

// PromptRules returns standard instructions to append to autonomous judge prompts.
func PromptRules() string {
	return `- Every judgment must include a "confidence" integer field from 1 to 10: how sure you are the result is correct and should be acted on automatically without user confirmation.
- Only include results you would rate confidence >= 7. Omit borderline or uncertain items.
- If nothing meets that bar, return an empty result (empty array / null / explicit negative) — never explanatory filler like "nothing found".`
}

// ParseJSONObject extracts a JSON object from model output, tolerating markdown fences
// and leading or trailing prose.
func ParseJSONObject(text string) ([]byte, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false
	}
	text = StripMarkdownJSONFence(text)
	if json.Valid([]byte(text)) && strings.HasPrefix(text, "{") {
		return []byte(text), true
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		slice := text[start : end+1]
		if json.Valid([]byte(slice)) {
			return []byte(slice), true
		}
	}
	return nil, false
}

// StripMarkdownJSONFence removes a surrounding ```json ... ``` block if present.
func StripMarkdownJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		first := strings.ToLower(strings.TrimSpace(s[:i]))
		if first == "json" || first == "javascript" || !strings.Contains(first, "{") {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return strings.TrimSpace(s)
}
