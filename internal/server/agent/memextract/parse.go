package memextract

import (
	"encoding/json"
	"strings"

	"github.com/ivoras/harlequin/internal/server/llm/judge"
)

// Prompt is the system prompt for autonomous memory extraction (uses judge.PromptRules).
var Prompt = `You decide whether a chat turn contains durable facts worth remembering long-term
(preferences, identity, organisation facts, ongoing projects, constraints). For shared scope,
only consider facts that are surprising and unknown, and avoid trivia or easily computable facts.

Respond with JSON only (no markdown, no commentary):
{"memories":[{"content":"...","confidence":N,"scope":"user"|"shared"}, ...]}

` + ScopeRules + `

Other rules:
- "content" is one terse fact in third person (not a quote of the assistant refusing to help).
` + judge.PromptRules() + `
- If nothing qualifies, respond exactly: {"memories":[]}
- Do not explain your reasoning. Do not output placeholder or meta text like "no facts found".`

// DocumentPrompt is the system prompt for distilling durable facts from an
// imported document (rather than a conversation turn). It targets the document's
// salient, specific facts — what it is and the entities, dates, identifiers,
// definitions, obligations or decisions it records — not personal/user facts.
var DocumentPrompt = `You extract durable, factual knowledge worth remembering long-term from an imported document.
Capture its salient, specific facts: what the document is, the organisations/people/parties involved,
key dates, identifiers, definitions, decisions or obligations. Prefer concrete, non-obvious facts over
generic or easily-computable ones. List discrete facts; do not summarise the whole document.

Respond with JSON only (no markdown, no commentary):
{"memories":[{"content":"...","confidence":N,"scope":"user"|"shared"}, ...]}

` + ScopeRules + `

Other rules:
- "content" is one terse fact in third person.
- Facts that are general knowledge from the document (not personal to one user) are "shared".
` + judge.PromptRules() + `
- If nothing qualifies, respond exactly: {"memories":[]}
- Do not explain your reasoning. Do not output placeholder or meta text like "no facts found".`

type Candidate struct {
	Content    string
	Scope      string // "user" or "shared"
	Confidence int
}

type response struct {
	Memories []struct {
		Content    string `json:"content"`
		Scope      string `json:"scope"`
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
	Scope      string `json:"scope"`
	Confidence int    `json:"confidence"`
}) []Candidate {
	out := make([]Candidate, 0, len(in))
	for _, c := range in {
		content := NormalizeFact(c.Content)
		if content == "" {
			continue
		}
		out = append(out, Candidate{
			Content:    content,
			Scope:      NormalizeScope(c.Scope),
			Confidence: judge.Clamp(c.Confidence),
		})
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
