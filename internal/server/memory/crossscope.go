package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/llm/judge"
)

// crossScopeShortlistK bounds how many existing keys from the other scope are
// offered to the same-attribute judge.
const crossScopeShortlistK = 8

// CrossScopeMatch is an existing slot in the opposite scope that denotes the same
// attribute as a proposed slot key — a write that would put the same attribute in
// both shared and personal memory, which is disallowed.
type CrossScopeMatch struct {
	ID    string // composite memory id in the other scope (u.N / s.N)
	Key   string // the existing slot key
	Value string // the existing slot value
	Exact bool   // true when the keys are identical (deterministic); false = judge-confirmed
}

// slotRef is one candidate slot considered for a same-attribute comparison.
type slotRef struct {
	local int64
	key   string
	value string
}

// CrossScopeSlot reports whether writing slot key into scope would duplicate an
// attribute that already exists in the OTHER scope: an identical key (always), or
// — when a judge is configured — a different key the LLM confirms is the same
// attribute (confidence >= 7). Returns nil when there is no cross-scope clash.
func (s *Store) CrossScopeSlot(ctx context.Context, userDB *sql.DB, scope, key string) (*CrossScopeMatch, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	if scope == "" {
		scope = scopeUser
	}
	otherScope := scopeShared
	if scope == scopeShared {
		otherScope = scopeUser
	}
	other := s.memFor(otherScope, userDB)
	if other.db == nil {
		return nil, nil
	}

	// 1) Exact key in the other scope — deterministic.
	if rows := other.slotsForKey(ctx, key); len(rows) > 0 {
		return &CrossScopeMatch{ID: other.encode(rows[0].memoryLocal), Key: key, Value: rows[0].value, Exact: true}, nil
	}

	// 2) Semantic: ask the judge whether the new key matches any existing key in
	// the other scope. Short key strings embed too close together for a reliable
	// distance threshold, so embeddings only shortlist; the judge decides.
	if s.judge == nil {
		return nil, nil
	}
	cands := other.allSlotRefs(ctx)
	if len(cands) == 0 {
		return nil, nil
	}
	if len(cands) > crossScopeShortlistK {
		blob, err := s.embed(ctx, HumanizeKey(key))
		if err == nil && blob != nil {
			if near := other.nearestSlotRefs(ctx, blob, crossScopeShortlistK); len(near) > 0 {
				cands = near
			}
		}
		if len(cands) > crossScopeShortlistK {
			cands = cands[:crossScopeShortlistK]
		}
	}
	idx, conf, ok := s.sameAttributeJudge(ctx, key, cands)
	if !ok || !judge.Accept(conf) || idx < 0 || idx >= len(cands) {
		return nil, nil
	}
	c := cands[idx]
	return &CrossScopeMatch{ID: other.encode(c.local), Key: c.key, Value: c.value, Exact: false}, nil
}

// sameAttributeJudge asks the LLM which candidate key (if any) denotes the same
// attribute as newKey. Returns the 0-based candidate index, the confidence, and
// whether a match was reported.
func (s *Store) sameAttributeJudge(ctx context.Context, newKey string, cands []slotRef) (int, int, bool) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "New attribute key: %q.\nCandidate existing keys:\n", newKey)
	for i, c := range cands {
		fmt.Fprintf(&sb, "%d. %s (e.g. %q)\n", i+1, c.key, capExample(c.value))
	}
	sb.WriteString(`Which candidate, if any, denotes the SAME real-world attribute as the new key (so they must not both exist)? Reply with a single JSON object {"match": <candidate number, or 0 if none>, "confidence": <1-10>}.`)

	sys := "You compare memory attribute keys. Two keys are the SAME attribute when they describe the same property of the same entity — e.g. \"company.name\" and \"organization.name\" are both the organisation's name. Keys about different things are NOT the same even if worded similarly. Reply with JSON only.\n" + judge.PromptRules()

	stream, err := s.judge.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: sys},
			{Role: llm.RoleUser, Content: sb.String()},
		},
		Temperature: llm.Ptr(0.0),
	})
	if err != nil {
		return -1, 0, false
	}
	var text string
	for chunk := range stream {
		if chunk.Err != nil {
			return -1, 0, false
		}
		text += chunk.TextDelta
	}
	raw, ok := judge.ParseJSONObject(text)
	if !ok {
		return -1, 0, false
	}
	var out struct {
		Match      int `json:"match"`
		Confidence int `json:"confidence"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return -1, 0, false
	}
	if out.Match < 1 || out.Match > len(cands) {
		return -1, 0, false
	}
	return out.Match - 1, judge.Clamp(out.Confidence), true
}

// allSlotRefs returns every slot in this database file.
func (m memDB) allSlotRefs(ctx context.Context) []slotRef {
	if m.db == nil {
		return nil
	}
	rows, err := m.db.QueryContext(ctx, `SELECT memory_id, key, value FROM memory_slots`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []slotRef
	for rows.Next() {
		var r slotRef
		if rows.Scan(&r.local, &r.key, &r.value) == nil {
			out = append(out, r)
		}
	}
	return out
}

// nearestSlotRefs returns up to k slots whose key embedding is closest to blob.
func (m memDB) nearestSlotRefs(ctx context.Context, blob any, k int) []slotRef {
	if m.db == nil || blob == nil {
		return nil
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT s.memory_id, s.key, s.value FROM memory_slots_vec v JOIN memory_slots s ON s.id = v.rowid
		 WHERE v.embedding MATCH ? AND k = ? ORDER BY v.distance`, blob, k)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []slotRef
	for rows.Next() {
		var r slotRef
		if rows.Scan(&r.local, &r.key, &r.value) == nil {
			out = append(out, r)
		}
	}
	return out
}
