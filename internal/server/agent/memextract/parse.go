package memextract

import (
	"encoding/json"
	"strings"

	"github.com/ivoras/harlequin/internal/server/llm/judge"
)

// Prompt is the system prompt for autonomous memory extraction (uses judge.PromptRules).
var Prompt = `You decide whether a chat turn contains durable facts about the user worth remembering long-term
(preferences, identity, ongoing projects, constraints, stable relationships).

Respond with JSON only (no markdown, no commentary):
{"memories":[{"content":"...","confidence":N}, ...]}

Rules:
- "content" is one terse fact in third person or about the user (not a quote of the assistant refusing to help).
` + judge.PromptRules() + `
- If nothing qualifies, respond exactly: {"memories":[]}
- Do not explain your reasoning. Do not output placeholder or meta text like "no facts found".`

type Candidate struct {
	Content    string
	Confidence int
}

type response struct {
	Memories []struct {
		Content    string `json:"content"`
		Confidence int    `json:"confidence"`
	} `json:"memories"`
}

// ParseResponse decodes the LLM extraction JSON and normalizes candidates.
func ParseResponse(text string) ([]Candidate, bool) {
	raw, ok := judge.ParseJSONObject(text)
	if !ok {
		return nil, false
	}
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, false
	}
	return filterCandidates(resp.Memories), true
}

func filterCandidates(in []struct {
	Content    string `json:"content"`
	Confidence int    `json:"confidence"`
}) []Candidate {
	out := make([]Candidate, 0, len(in))
	for _, c := range in {
		content := NormalizeFact(c.Content)
		if content == "" {
			continue
		}
		out = append(out, Candidate{Content: content, Confidence: judge.Clamp(c.Confidence)})
	}
	return out
}

// NormalizeFact trims and rejects meta/placeholder extraction output.
func NormalizeFact(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "-* \t")
	s = strings.TrimSpace(s)
	if s == "" || len(s) < 4 {
		return ""
	}
	if isRejectedFact(s) {
		return ""
	}
	return s
}

func isRejectedFact(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	lower = strings.Trim(lower, "\"'`")
	if lower == "" || lower == "none" || lower == "n/a" || lower == "null" {
		return true
	}
	rejects := []string{
		"no durable facts",
		"no facts",
		"nothing to extract",
		"nothing durable",
		"no memories",
		"no memory",
		"not applicable",
	}
	for _, r := range rejects {
		if strings.Contains(lower, r) {
			return true
		}
	}
	if strings.HasPrefix(lower, "there are no ") || strings.HasPrefix(lower, "there is no ") {
		return true
	}
	return false
}

// ShouldStore returns true when a candidate meets the autonomous judgment threshold.
func ShouldStore(c Candidate) bool {
	return judge.Accept(c.Confidence) && NormalizeFact(c.Content) != ""
}
