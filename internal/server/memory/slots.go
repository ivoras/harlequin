package memory

import (
	"context"
	"database/sql"
	"strings"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/memory/slotextract"
)

const (
	// slotKeyCanonicalThreshold is the number of distinct slot keys (counted
	// across the user and shared databases) at or above which we stop sending
	// the extractor every existing key and instead send only the most similar
	// ones (retrieved by key-embedding similarity).
	slotKeyCanonicalThreshold = 100
	// slotKeyTopK is how many similar keys to offer the extractor per database
	// in the retrieval (large key set) regime.
	slotKeyTopK = 12
)

// extractSlot asks the LLM to distill a normalized (key, value) slot from
// content, offering existing keys for reuse. contentBlob is content's embedding
// (used to retrieve similar keys once the key set is large); it may be nil.
func (s *Store) extractSlot(ctx context.Context, userDB *sql.DB, content string, contentBlob any) (slotextract.Slot, bool) {
	if s.judge == nil {
		return slotextract.Slot{}, false
	}
	keys := s.candidateKeys(ctx, userDB, contentBlob)
	stream, err := s.judge.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: slotextract.Prompt},
			{Role: llm.RoleUser, Content: slotextract.BuildUserPrompt(keys, content)},
		},
		Temperature: llm.Ptr(0.0),
	})
	if err != nil {
		return slotextract.Slot{}, false
	}
	var text string
	for chunk := range stream {
		if chunk.Err != nil {
			return slotextract.Slot{}, false
		}
		text += chunk.TextDelta
	}
	return slotextract.Parse(text)
}

// candidateKeys gathers existing slot keys (across the user and shared
// databases) to offer the extractor for canonicalization. While the total
// distinct key count is below slotKeyCanonicalThreshold it returns all of them;
// past it, only the keys most similar to contentBlob.
func (s *Store) candidateKeys(ctx context.Context, userDB *sql.DB, contentBlob any) []string {
	mems := []memDB{s.userMem(userDB), s.sharedMem()}

	total := 0
	for _, m := range mems {
		total += m.distinctKeyCount(ctx)
	}

	seen := map[string]bool{}
	var out []string
	add := func(keys []string) {
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}

	if total < slotKeyCanonicalThreshold {
		for _, m := range mems {
			add(m.distinctKeys(ctx))
		}
		return out
	}
	if contentBlob == nil {
		return out
	}
	for _, m := range mems {
		add(m.keysNear(ctx, contentBlob, slotKeyTopK))
	}
	return out
}

// storeSlot persists a memory's slot and indexes its key embedding.
func (s *Store) storeSlot(ctx context.Context, userDB *sql.DB, memID string, slot slotextract.Slot) {
	scope, local, ok := decodeID(memID)
	if !ok {
		return
	}
	keyBlob, _ := s.embed(ctx, slot.Key)
	_ = s.memFor(scope, userDB).insertSlot(ctx, local, slot.Key, slot.Value, keyBlob)
}

// slotConflicts records duplicate/conflict pairs for existing memories that
// share the new memory's slot key (across the user and shared databases) and
// returns them. Same key + same value is a duplicate; same key + different
// value is a conflict — the deterministic signal a free-text judge cannot give.
func (s *Store) slotConflicts(ctx context.Context, userDB *sql.DB, newID string, slot slotextract.Slot) []ConflictHit {
	var hits []ConflictHit
	for _, m := range []memDB{s.userMem(userDB), s.sharedMem()} {
		for _, row := range m.slotsForKey(ctx, slot.Key) {
			other := m.encode(row.memoryLocal)
			if other == newID {
				continue
			}
			rel, reason, conf := "conflicts", `Same attribute "`+slot.Key+`" with a different value`, 9
			if strings.EqualFold(strings.TrimSpace(row.value), strings.TrimSpace(slot.Value)) {
				rel, reason, conf = "duplicate", `Same attribute "`+slot.Key+`" with the same value`, 10
			}
			_ = s.recordConflict(ctx, userDB, newID, other, rel, reason, conf)
			hits = append(hits, ConflictHit{
				OtherID:      other,
				OtherContent: s.contentFor(ctx, userDB, other),
				Relationship: rel,
				Reason:       reason,
				Confidence:   conf,
			})
		}
	}
	return hits
}

// slotRow is one (memory_id, value) row from memory_slots.
type slotRow struct {
	memoryLocal int64
	value       string
}

// insertSlot writes a slot and its key-embedding index row.
func (m memDB) insertSlot(ctx context.Context, memoryLocal int64, key, value string, keyBlob any) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO memory_slots(memory_id, key, value) VALUES (?, ?, ?)`, memoryLocal, key, value)
	if err != nil {
		return err
	}
	if keyBlob != nil {
		slotID, _ := res.LastInsertId()
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_slots_vec(rowid, embedding) VALUES (?, ?)`, slotID, keyBlob); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m memDB) distinctKeyCount(ctx context.Context) int {
	if m.db == nil {
		return 0
	}
	var n int
	_ = m.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT key) FROM memory_slots`).Scan(&n)
	return n
}

func (m memDB) distinctKeys(ctx context.Context) []string {
	if m.db == nil {
		return nil
	}
	rows, err := m.db.QueryContext(ctx, `SELECT DISTINCT key FROM memory_slots ORDER BY key`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out = append(out, k)
		}
	}
	return out
}

// keysNear returns up to k distinct keys whose embeddings are closest to blob.
func (m memDB) keysNear(ctx context.Context, blob any, k int) []string {
	if m.db == nil || blob == nil {
		return nil
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT s.key FROM memory_slots_vec v JOIN memory_slots s ON s.id = v.rowid
		 WHERE v.embedding MATCH ? AND k = ? ORDER BY v.distance`, blob, k)
	if err != nil {
		return nil
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var key string
		if rows.Scan(&key) == nil && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

// slotsForKey returns all (memory_id, value) rows for a key in this database.
func (m memDB) slotsForKey(ctx context.Context, key string) []slotRow {
	if m.db == nil {
		return nil
	}
	rows, err := m.db.QueryContext(ctx, `SELECT memory_id, value FROM memory_slots WHERE key = ?`, key)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []slotRow
	for rows.Next() {
		var r slotRow
		if rows.Scan(&r.memoryLocal, &r.value) == nil {
			out = append(out, r)
		}
	}
	return out
}

// execQuerier is satisfied by both *sql.DB and *sql.Tx.
type execQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// deleteSlots removes a memory's slot rows and their index rows. The slot-id
// cursor is fully read before any delete, since each handle has a single
// connection. Safe to use with a *sql.DB or a *sql.Tx.
func deleteSlots(ctx context.Context, eq execQuerier, memoryLocal int64) error {
	rows, err := eq.QueryContext(ctx, `SELECT id FROM memory_slots WHERE memory_id = ?`, memoryLocal)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		_, _ = eq.ExecContext(ctx, `DELETE FROM memory_slots_vec WHERE rowid = ?`, id)
	}
	_, _ = eq.ExecContext(ctx, `DELETE FROM memory_slots WHERE memory_id = ?`, memoryLocal)
	return nil
}
