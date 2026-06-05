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
- You are given the keys already in use. Reuse one EXACTLY only if it names the same kind of attribute about the same subject as this memory (e.g. do not reuse "company.domain_candidate" for a product's price). Otherwise invent a new key; when unsure, invent a new key rather than forcing a fit.
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

// KeyExample is an existing slot key offered to the extractor for reuse, with a
// representative value so the model can tell what attribute the key denotes
// (e.g. that "company.domain_candidate" is about domains, not prices).
type KeyExample struct {
	Key     string
	Example string // a sample value; may be empty
}

// BuildUserPrompt renders the user message: the candidate keys to reuse (each
// with an example value, when known) plus the memory to extract from.
func BuildUserPrompt(candidateKeys []KeyExample, content string) string {
	var b strings.Builder
	if len(candidateKeys) == 0 {
		b.WriteString("Keys already in use: (none yet)")
	} else {
		b.WriteString("Keys already in use:")
		for _, k := range candidateKeys {
			b.WriteString("\n- ")
			b.WriteString(k.Key)
			if k.Example != "" {
				b.WriteString(` (e.g. "`)
				b.WriteString(k.Example)
				b.WriteString(`")`)
			}
		}
	}
	b.WriteString("\n\nMemory:\n")
	b.WriteString(content)
	return b.String()
}
