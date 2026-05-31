// Package slotextract distills a single normalized (key, value) "slot" from a
// memory's text using the LLM. The key is a canonical dotted attribute name
// (e.g. "company.name"); reusing the same key for the same attribute is what
// lets duplicate/conflict detection compare values deterministically.
package slotextract

import (
	"encoding/json"
	"strings"

	"github.com/ivoras/harlequin/internal/server/llm/judge"
)

// Prompt is the system prompt for slot extraction.
var Prompt = `You extract at most one durable factual "slot" from a single stored memory and normalize it to a key/value pair.

Respond with JSON only (no markdown, no commentary):
{"key":"<dotted.key>","value":"<value>"}
If the memory is not a single factual attribute (e.g. a preference list, an event, or a free-form note), respond {"key":"","value":""}.

Rules:
- "key" is a short lowercase dotted path naming the attribute, e.g. "company.name", "user.timezone", "project.deadline". Use only the characters a-z, 0-9, "." and "_".
- You are given the keys already in use. If one of them denotes the SAME attribute as this memory, REUSE it exactly; only invent a new key when none fit.
- "value" is the attribute's value as a short plain string (no surrounding quotes, no trailing punctuation).
- Extract the single most salient attribute; never invent facts not present in the memory.`

// Slot is a normalized attribute extracted from a memory.
type Slot struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Parse extracts a slot from the model's response. ok is false when no usable
// slot was produced (empty key or value).
func Parse(text string) (Slot, bool) {
	raw, ok := judge.ParseJSONObject(text)
	if !ok {
		return Slot{}, false
	}
	var s Slot
	if err := json.Unmarshal(raw, &s); err != nil {
		return Slot{}, false
	}
	s.Key = NormalizeKey(s.Key)
	s.Value = strings.TrimSpace(s.Value)
	if s.Key == "" || s.Value == "" {
		return Slot{}, false
	}
	if len(s.Key) > 120 {
		s.Key = s.Key[:120]
	}
	if len(s.Value) > 500 {
		s.Value = s.Value[:500]
	}
	return s, true
}

// NormalizeKey lowercases the key and keeps only [a-z0-9._], trimming dots.
func NormalizeKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	var b strings.Builder
	for _, r := range k {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), ".")
}

// BuildUserPrompt renders the user message: the candidate keys to reuse plus the
// memory to extract from.
func BuildUserPrompt(candidateKeys []string, content string) string {
	keys := "(none yet)"
	if len(candidateKeys) > 0 {
		keys = strings.Join(candidateKeys, ", ")
	}
	return "Keys already in use: " + keys + "\n\nMemory:\n" + content
}
