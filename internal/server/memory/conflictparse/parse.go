package conflictparse

import (
	"encoding/json"

	"github.com/ivoras/harlequin/internal/server/llm/judge"
)

// Prompt is the system prompt for batch memory-pair judgment.
var Prompt = `You compare a newly stored memory against existing candidate memories for the same user/org context.

Respond with JSON only (no markdown, no commentary):
{"judgments":[{"other_id":"ID","relationship":"none|duplicate|supersedes|conflicts","confidence":N,"reason":"..."}]}

Rules:
- "other_id" is the candidate memory id string from the prompt (e.g. "u.7" or "s.3").
- "relationship":
  - "none" — compatible or unrelated; no action.
  - "duplicate" — same fact phrased differently.
  - "supersedes" — the new memory replaces/outdates the candidate (not a contradiction).
  - "conflicts" — both cannot be true at once.
- Include a judgment only when relationship is "duplicate" or "conflicts" AND confidence >= 7.
- "reason" is one short sentence explaining the call.
` + judge.PromptRules()

type Judgment struct {
	OtherID      string `json:"other_id"`
	Relationship string `json:"relationship"`
	Confidence   int    `json:"confidence"`
	Reason       string `json:"reason"`
}

type response struct {
	Judgments []Judgment `json:"judgments"`
}

// Flagged returns duplicate/conflicts judgments meeting the confidence threshold.
func Flagged(text string) ([]Judgment, bool) {
	raw, ok := judge.ParseJSONObject(text)
	if !ok {
		return nil, false
	}
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, false
	}
	var out []Judgment
	for _, j := range resp.Judgments {
		if j.OtherID == "" {
			continue
		}
		rel := j.Relationship
		if rel != "duplicate" && rel != "conflicts" {
			continue
		}
		if !judge.Accept(j.Confidence) {
			continue
		}
		reason := j.Reason
		if len(reason) > 500 {
			reason = reason[:500]
		}
		out = append(out, Judgment{
			OtherID:      j.OtherID,
			Relationship: rel,
			Confidence:   judge.Clamp(j.Confidence),
			Reason:       reason,
		})
	}
	return out, true
}
