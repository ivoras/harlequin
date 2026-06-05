package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/memory/conflictparse"
	"github.com/ivoras/harlequin/internal/server/memory/slotextract"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const defaultConflictCandidates = 8

const dupReason = "Exact/near-exact duplicate text"

// ConflictHit describes one existing memory found to conflict with or duplicate
// a newly stored memory. OtherID is a composite id.
type ConflictHit struct {
	OtherID      string
	OtherContent string
	Relationship string // "conflicts" | "duplicate"
	Reason       string
	Confidence   int
	Key          string // shared slot key, when the conflict came from the structured slot path
}

// SetConflictJudge enables conflict detection on memory writes using the LLM.
func (s *Store) SetConflictJudge(p llm.Provider, candidateLimit int) {
	s.judge = p
	if candidateLimit <= 0 {
		candidateLimit = defaultConflictCandidates
	}
	s.conflictCandidates = candidateLimit
}

// detectConflicts compares a newly stored memory against existing candidates
// from both the user and shared databases, records any duplicate/conflict
// pairs, and returns them. It is a no-op (nil, nil) when no judge is configured.
func (s *Store) detectConflicts(ctx context.Context, userDB *sql.DB, userID int64, newID, content string) ([]ConflictHit, error) {
	if s.judge == nil {
		return nil, nil
	}
	limit := s.conflictCandidates
	if limit <= 0 {
		limit = defaultConflictCandidates
	}
	// content's embedding is reused for slot-key retrieval (large key set regime).
	contentBlob, _ := s.embed(ctx, content)

	candidates, err := s.Search(ctx, userDB, content, userID, "", limit+1)
	if err != nil {
		return nil, err
	}

	var hits []ConflictHit
	contentByID := map[string]string{}
	var lines []string
	for _, c := range candidates {
		if c.ID == newID {
			continue
		}
		contentByID[c.ID] = c.Content
		if strings.EqualFold(strings.TrimSpace(c.Content), strings.TrimSpace(content)) {
			_ = s.recordConflict(ctx, userDB, newID, c.ID, "duplicate", dupReason, 10)
			hits = append(hits, ConflictHit{OtherID: c.ID, OtherContent: c.Content, Relationship: "duplicate", Reason: dupReason, Confidence: 10})
			continue
		}
		lines = append(lines, fmt.Sprintf("- id %s: %s", c.ID, c.Content))
		if len(lines) >= limit {
			break
		}
	}

	// Structured slot path: use an already-indexed slot (from add/indexSlot) or
	// extract and index now, then flag peers sharing that key.
	var slot slotextract.Slot
	hasSlot := false
	if scope, local, ok := decodeID(newID); ok {
		slot, hasSlot = s.memFor(scope, userDB).slotForMemory(ctx, local)
	}
	if !hasSlot {
		slot, hasSlot = s.indexSlot(ctx, userDB, newID, content, contentBlob)
	}
	if hasSlot {
		hits = appendNewHits(hits, s.slotConflicts(ctx, userDB, newID, slot))
		return hits, nil
	}

	if len(lines) == 0 {
		return hits, nil
	}

	userPrompt := fmt.Sprintf("New memory (id %s):\n%s\n\nExisting candidates:\n%s",
		newID, content, strings.Join(lines, "\n"))
	text, err := s.judgeChat(ctx, userPrompt)
	if err != nil {
		return hits, err
	}
	judgments, ok := conflictparse.Flagged(text)
	if !ok {
		return hits, nil
	}
	for _, j := range judgments {
		if j.OtherID == newID {
			continue
		}
		_ = s.recordConflict(ctx, userDB, newID, j.OtherID, j.Relationship, j.Reason, j.Confidence)
		hits = append(hits, ConflictHit{OtherID: j.OtherID, OtherContent: contentByID[j.OtherID], Relationship: j.Relationship, Reason: j.Reason, Confidence: j.Confidence})
	}
	return hits, nil
}

// appendNewHits appends hits whose OtherID is not already present in base
// (e.g. a memory already flagged as an exact-text duplicate).
func appendNewHits(base, extra []ConflictHit) []ConflictHit {
	seen := make(map[string]bool, len(base))
	for _, h := range base {
		seen[h.OtherID] = true
	}
	for _, h := range extra {
		if !seen[h.OtherID] {
			seen[h.OtherID] = true
			base = append(base, h)
		}
	}
	return base
}

// judgeChat runs the conflict-judge prompt and returns the model's text.
func (s *Store) judgeChat(ctx context.Context, userPrompt string) (string, error) {
	stream, err := s.judge.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: conflictparse.Prompt},
			{Role: llm.RoleUser, Content: userPrompt},
		},
		Temperature: llm.Ptr(0.0),
	})
	if err != nil {
		return "", err
	}
	var text string
	for chunk := range stream {
		if chunk.Err != nil {
			return "", chunk.Err
		}
		text += chunk.TextDelta
	}
	return text, nil
}

// conflictHome selects the database that stores a conflict pair: the user
// database if either endpoint is a user memory, otherwise the shared database.
func (s *Store) conflictHome(userDB *sql.DB, idA, idB string) memDB {
	if sa, _, _ := decodeID(idA); sa == scopeUser {
		return s.userMem(userDB)
	}
	if sb, _, _ := decodeID(idB); sb == scopeUser {
		return s.userMem(userDB)
	}
	return s.sharedMem()
}

func (s *Store) recordConflict(ctx context.Context, userDB *sql.DB, idA, idB, relationship, reason string, confidence int) error {
	if idA == idB {
		return nil
	}
	a, b := idA, idB
	if a > b { // canonical order satisfies CHECK (memory_a < memory_b)
		a, b = b, a
	}
	_, err := s.conflictHome(userDB, a, b).db.ExecContext(ctx, `
		INSERT INTO memory_conflicts(memory_a, memory_b, relationship, reason, confidence)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(memory_a, memory_b) DO UPDATE SET
			relationship = excluded.relationship,
			reason = excluded.reason,
			confidence = excluded.confidence,
			detected_at = CURRENT_TIMESTAMP,
			resolved_at = NULL`,
		a, b, relationship, reason, confidence)
	return err
}

// deleteConflictsFor removes conflict rows referencing a memory from both
// reachable databases (replacing the FK cascade that cross-file refs preclude).
func (s *Store) deleteConflictsFor(ctx context.Context, userDB *sql.DB, id string) {
	for _, m := range []memDB{s.userMem(userDB), s.sharedMem()} {
		if m.db == nil {
			continue
		}
		_, _ = m.db.ExecContext(ctx, `DELETE FROM memory_conflicts WHERE memory_a = ? OR memory_b = ?`, id, id)
	}
}

// ListConflicts returns unresolved conflicts visible to the user: shared–shared
// pairs from the shared database plus any pair involving the user's memories
// from their database.
func (s *Store) ListConflicts(ctx context.Context, userDB *sql.DB, userID int64, limit int) ([]types.MemoryConflict, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []types.MemoryConflict
	for _, home := range []memDB{s.sharedMem(), s.userMem(userDB)} {
		cs, err := s.listConflicts(ctx, home, userDB, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	return out, nil
}

func (s *Store) listConflicts(ctx context.Context, home memDB, userDB *sql.DB, limit int) ([]types.MemoryConflict, error) {
	if home.db == nil {
		return nil, nil
	}
	rows, err := home.db.QueryContext(ctx, `
		SELECT id, memory_a, memory_b, relationship, reason, confidence, detected_at, resolved_at
		FROM memory_conflicts WHERE resolved_at IS NULL
		ORDER BY detected_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	var out []types.MemoryConflict
	for rows.Next() {
		var rowID int64
		var c types.MemoryConflict
		var resolved sql.NullTime
		if err := rows.Scan(&rowID, &c.MemoryA, &c.MemoryB, &c.Relationship,
			&c.Reason, &c.Confidence, &c.DetectedAt, &resolved); err != nil {
			rows.Close()
			return nil, err
		}
		c.ID = home.encode(rowID)
		if resolved.Valid {
			c.ResolvedAt = &resolved.Time
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	// Resolve endpoint contents only after the cursor is closed: each database
	// handle allows a single connection, so a nested query during iteration
	// would deadlock.
	for i := range out {
		out[i].ContentA = s.contentFor(ctx, userDB, out[i].MemoryA)
		out[i].ContentB = s.contentFor(ctx, userDB, out[i].MemoryB)
	}
	return out, nil
}

// contentFor resolves a composite memory id to its content, or "(deleted)".
func (s *Store) contentFor(ctx context.Context, userDB *sql.DB, id string) string {
	scope, local, ok := decodeID(id)
	if !ok {
		return "(deleted)"
	}
	var content string
	if err := s.memFor(scope, userDB).db.QueryRowContext(ctx,
		`SELECT content FROM memories WHERE id = ?`, local).Scan(&content); err != nil {
		return "(deleted)"
	}
	return content
}

// ResolveConflict marks a conflict resolved. The conflict id encodes which
// database holds the row.
func (s *Store) ResolveConflict(ctx context.Context, userDB *sql.DB, conflictID string) error {
	scope, local, ok := decodeID(conflictID)
	if !ok {
		return ErrNotFound
	}
	res, err := s.memFor(scope, userDB).db.ExecContext(ctx,
		`UPDATE memory_conflicts SET resolved_at = CURRENT_TIMESTAMP WHERE id = ? AND resolved_at IS NULL`, local)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
