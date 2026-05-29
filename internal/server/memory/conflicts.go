package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/memory/conflictparse"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const defaultConflictCandidates = 8

// SetConflictJudge enables background conflict detection after Add using the LLM.
func (s *Store) SetConflictJudge(p llm.Provider, candidateLimit int) {
	s.judge = p
	if candidateLimit <= 0 {
		candidateLimit = defaultConflictCandidates
	}
	s.conflictCandidates = candidateLimit
}

func (s *Store) checkConflictsAsync(userID, newID int64, content string) {
	if s.judge == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_ = s.checkConflicts(ctx, userID, newID, content)
	}()
}

func (s *Store) checkConflicts(ctx context.Context, userID, newID int64, content string) error {
	limit := s.conflictCandidates
	if limit <= 0 {
		limit = defaultConflictCandidates
	}
	candidates, err := s.Search(ctx, content, userID, "", limit+1)
	if err != nil {
		return err
	}

	var lines []string
	for _, c := range candidates {
		if c.ID == newID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(c.Content), strings.TrimSpace(content)) {
			_ = s.recordConflict(ctx, newID, c.ID, "duplicate", "Exact/near-exact duplicate text", 10)
			continue
		}
		lines = append(lines, fmt.Sprintf("- id %d: %s", c.ID, c.Content))
		if len(lines) >= limit {
			break
		}
	}
	if len(lines) == 0 {
		return nil
	}

	userPrompt := fmt.Sprintf("New memory (id %d):\n%s\n\nExisting candidates:\n%s",
		newID, content, strings.Join(lines, "\n"))

	stream, err := s.judge.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: conflictparse.Prompt},
			{Role: llm.RoleUser, Content: userPrompt},
		},
		Temperature: llm.Ptr(0.0),
	})
	if err != nil {
		return err
	}
	var text string
	for chunk := range stream {
		if chunk.Err != nil {
			return chunk.Err
		}
		text += chunk.TextDelta
	}

	judgments, ok := conflictparse.Flagged(text)
	if !ok {
		return nil
	}
	for _, j := range judgments {
		if j.OtherID == newID {
			continue
		}
		_ = s.recordConflict(ctx, newID, j.OtherID, j.Relationship, j.Reason, j.Confidence)
	}
	return nil
}

func pairIDs(a, b int64) (int64, int64) {
	if a < b {
		return a, b
	}
	return b, a
}

func (s *Store) recordConflict(ctx context.Context, idA, idB int64, relationship, reason string, confidence int) error {
	a, b := pairIDs(idA, idB)
	_, err := s.db.ExecContext(ctx, `
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

// ListConflicts returns unresolved conflicts visible to the user.
func (s *Store) ListConflicts(ctx context.Context, userID int64, limit int) ([]types.MemoryConflict, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.memory_a, c.memory_b, ma.content, mb.content,
			c.relationship, c.reason, c.confidence, c.detected_at, c.resolved_at
		FROM memory_conflicts c
		JOIN memories ma ON ma.id = c.memory_a
		JOIN memories mb ON mb.id = c.memory_b
		WHERE c.resolved_at IS NULL
		  AND (ma.scope = 'shared' OR (ma.scope = 'user' AND ma.user_id = ?))
		  AND (mb.scope = 'shared' OR (mb.scope = 'user' AND mb.user_id = ?))
		ORDER BY c.detected_at DESC
		LIMIT ?`, userID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.MemoryConflict
	for rows.Next() {
		var c types.MemoryConflict
		var resolved sql.NullTime
		if err := rows.Scan(&c.ID, &c.MemoryA, &c.MemoryB, &c.ContentA, &c.ContentB,
			&c.Relationship, &c.Reason, &c.Confidence, &c.DetectedAt, &resolved); err != nil {
			return nil, err
		}
		if resolved.Valid {
			c.ResolvedAt = &resolved.Time
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ResolveConflict marks a conflict resolved if both memories are visible to the user.
func (s *Store) ResolveConflict(ctx context.Context, conflictID, userID int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE memory_conflicts SET resolved_at = CURRENT_TIMESTAMP
		WHERE id = ? AND resolved_at IS NULL
		  AND EXISTS (
		    SELECT 1 FROM memories ma, memories mb
		    WHERE ma.id = memory_conflicts.memory_a AND mb.id = memory_conflicts.memory_b
		      AND (ma.scope = 'shared' OR (ma.scope = 'user' AND ma.user_id = ?))
		      AND (mb.scope = 'shared' OR (mb.scope = 'user' AND mb.user_id = ?))
		  )`, conflictID, userID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ConflictCountForMemory returns unresolved conflict count involving a memory id.
func (s *Store) ConflictCountForMemory(ctx context.Context, memoryID, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM memory_conflicts c
		JOIN memories ma ON ma.id = c.memory_a
		JOIN memories mb ON mb.id = c.memory_b
		WHERE c.resolved_at IS NULL
		  AND (c.memory_a = ? OR c.memory_b = ?)
		  AND (ma.scope = 'shared' OR (ma.scope = 'user' AND ma.user_id = ?))
		  AND (mb.scope = 'shared' OR (mb.scope = 'user' AND mb.user_id = ?))`,
		memoryID, memoryID, userID, userID).Scan(&n)
	return n, err
}
