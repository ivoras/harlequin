package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/memory/slotextract"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const (
	// slotKeyTopK is how many similar existing keys to offer the extractor per
	// database, retrieved by key-embedding similarity to the new memory. Offering
	// only nearby keys (rather than every key) keeps the prompt bounded and avoids
	// tempting the model to reuse an unrelated key.
	slotKeyTopK = 12
	// slotKeyExampleMax caps the length of the example value shown next to a
	// candidate key, so one long value can't bloat the prompt.
	slotKeyExampleMax = 40
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

// candidateKeys gathers the existing slot keys most similar to the new memory
// (across the user and shared databases), each with an example value, to offer
// the extractor for canonicalization. Restricting to nearby keys keeps the
// prompt bounded and reduces mis-reuse of unrelated keys. When contentBlob is
// nil (embedding unavailable) it falls back to all distinct keys without
// examples so canonicalization still has something to work with.
func (s *Store) candidateKeys(ctx context.Context, userDB *sql.DB, contentBlob any) []slotextract.KeyExample {
	mems := []memDB{s.userMem(userDB), s.sharedMem()}
	seen := map[string]bool{}
	var out []slotextract.KeyExample

	if contentBlob == nil {
		for _, m := range mems {
			for _, k := range m.distinctKeys(ctx) {
				if !seen[k] {
					seen[k] = true
					out = append(out, slotextract.KeyExample{Key: k})
				}
			}
		}
		return out
	}
	for _, m := range mems {
		for _, ke := range m.keysNearWithExample(ctx, contentBlob, slotKeyTopK) {
			if !seen[ke.Key] {
				seen[ke.Key] = true
				out = append(out, ke)
			}
		}
	}
	return out
}

// indexSlot extracts a normalized slot for content and persists it. contentBlob
// may be content's embedding (or nil to compute). Returns ok false when no judge
// or no usable slot was produced.
func (s *Store) indexSlot(ctx context.Context, userDB *sql.DB, memID, content string, contentBlob any) (slotextract.Slot, bool) {
	if s.judge == nil {
		return slotextract.Slot{}, false
	}
	if contentBlob == nil {
		contentBlob, _ = s.embed(ctx, content)
	}
	slot, ok := s.extractSlot(ctx, userDB, content, contentBlob)
	if !ok {
		return slotextract.Slot{}, false
	}
	s.storeSlot(ctx, userDB, memID, slot)
	return slot, true
}

// storeSlot persists a memory's slot and indexes its key embedding.
func (s *Store) storeSlot(ctx context.Context, userDB *sql.DB, memID string, slot slotextract.Slot) {
	scope, local, ok := decodeID(memID)
	if !ok {
		return
	}
	// Embed the humanized form of the key (e.g. "organisation name") so it
	// compares well to natural-language queries; the stored key column keeps the
	// canonical dotted form used for exact-match conflict detection.
	keyBlob, _ := s.embed(ctx, HumanizeKey(slot.Key))
	_ = s.memFor(scope, userDB).insertSlot(ctx, local, slot.Key, slot.Value, keyBlob)
}

// AddSlot attaches a known (key, value) slot to an existing memory and indexes
// its humanized key embedding. For imports and evaluation where slots are
// supplied directly rather than LLM-extracted.
func (s *Store) AddSlot(ctx context.Context, userDB *sql.DB, memID, key, value string) error {
	return s.AddSlotEmbed(ctx, userDB, memID, key, value, HumanizeKey(key))
}

// AddSlotEmbed attaches a slot whose vector is the embedding of embedText rather
// than the default humanized key. For evaluating alternative slot-vector schemes
// (e.g. key+value); production code uses AddSlot.
func (s *Store) AddSlotEmbed(ctx context.Context, userDB *sql.DB, memID, key, value, embedText string) error {
	scope, local, ok := decodeID(memID)
	if !ok {
		return fmt.Errorf("invalid memory id %q", memID)
	}
	blob, err := s.embed(ctx, embedText)
	if err != nil {
		return fmt.Errorf("embed slot vector: %w", err)
	}
	return s.memFor(scope, userDB).insertSlot(ctx, local, key, value, blob)
}

// WriteSlot sets a single-valued slot to (key, content), idempotently. If a
// memory in the target scope already carries this slot key, its content and slot
// value are updated in place (and any duplicate carriers are collapsed into the
// oldest one); otherwise a new memory + slot is created. Returns the surviving
// memory id and whether it was newly created. This keeps keyed memory_write
// repeatable: writing the same key twice updates one memory instead of piling up
// duplicate entries.
func (s *Store) WriteSlot(ctx context.Context, userDB *sql.DB, scope, key, content string, userID int64, canManageShared bool) (string, bool, error) {
	key = strings.TrimSpace(key)
	if scope == "" {
		scope = scopeUser
	}
	m := s.memFor(scope, userDB)
	rows := m.slotsForKey(ctx, key)
	if len(rows) == 0 {
		mem, err := s.add(ctx, userDB, types.CreateMemoryRequest{Scope: scope, Content: content, Source: "tool"}, userID)
		if err != nil {
			return "", false, err
		}
		if err := s.AddSlot(ctx, userDB, mem.ID, key, content); err != nil {
			return "", false, err
		}
		return mem.ID, true, nil
	}
	// Keep the oldest carrier; update it; delete any others so the slot stays
	// single-valued.
	keepLocal := rows[0].memoryLocal
	for _, r := range rows[1:] {
		if r.memoryLocal < keepLocal {
			keepLocal = r.memoryLocal
		}
	}
	keepID := m.encode(keepLocal)
	blob, err := s.embed(ctx, content)
	if err != nil {
		return "", false, err
	}
	// updateContent re-indexes content and clears the memory's slots; re-attach
	// the slot afterwards so the (key, value) survives with the new value.
	if _, err := m.updateContent(ctx, keepLocal, content, blob); err != nil {
		return "", false, err
	}
	if err := s.AddSlot(ctx, userDB, keepID, key, content); err != nil {
		return "", false, err
	}
	s.deleteConflictsFor(ctx, userDB, keepID)
	for _, r := range rows {
		if r.memoryLocal != keepLocal {
			_ = s.Delete(ctx, userDB, m.encode(r.memoryLocal), userID, canManageShared)
		}
	}
	return keepID, false, nil
}

// HumanizeKey turns a canonical slot key into a natural-language phrase for
// embedding: dot/underscore/hyphen/slash separators become spaces and camelCase
// boundaries are split, lowercased (e.g. "organisation.name" -> "organisation
// name", "preferredCurrency" -> "preferred currency").
func HumanizeKey(key string) string {
	var b strings.Builder
	prevAlnum := false
	for _, r := range key {
		switch {
		case r == '.' || r == '_' || r == '-' || r == '/':
			b.WriteByte(' ')
			prevAlnum = false
		case unicode.IsUpper(r):
			if prevAlnum {
				b.WriteByte(' ')
			}
			b.WriteRune(unicode.ToLower(r))
			prevAlnum = true
		default:
			b.WriteRune(r)
			prevAlnum = unicode.IsLetter(r) || unicode.IsDigit(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// BackfillSlotKeyEmbeddings re-embeds every slot key in db using the humanized
// form and rewrites its memory_slots_vec row. Use after changing the key
// embedding scheme. Returns the number of slots reindexed.
func (s *Store) BackfillSlotKeyEmbeddings(ctx context.Context, db *sql.DB) (int, error) {
	if db == nil {
		return 0, nil
	}
	// Read all (id, key) first, then re-embed/rewrite — nested queries on a
	// single-connection sqlite handle would otherwise deadlock.
	rows, err := db.QueryContext(ctx, `SELECT id, key FROM memory_slots`)
	if err != nil {
		return 0, err
	}
	type slotKey struct {
		id  int64
		key string
	}
	var all []slotKey
	for rows.Next() {
		var sk slotKey
		if err := rows.Scan(&sk.id, &sk.key); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, sk)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, sk := range all {
		blob, err := s.embed(ctx, HumanizeKey(sk.key))
		if err != nil || blob == nil {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return n, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_slots_vec WHERE rowid = ?`, sk.id); err != nil {
			tx.Rollback()
			return n, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_slots_vec(rowid, embedding) VALUES (?, ?)`, sk.id, blob); err != nil {
			tx.Rollback()
			return n, err
		}
		if err := tx.Commit(); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// SlotMemory pairs a memory with its slot key/value (for GLOB key lookups).
type SlotMemory struct {
	ID      string // composite memory id (u.N / s.N)
	Key     string // slot key
	Value   string // slot value
	Content string // memory content
}

// MemoriesBySlotGlob returns memories whose slot key matches a GLOB pattern
// (e.g. "user.*"), across the given scope ("user"|"shared"|"" for both), newest
// first. GLOB is used so the BINARY index on memory_slots(key) serves prefix
// patterns as a range seek (LIKE would not — see the slot-key index notes).
func (s *Store) MemoriesBySlotGlob(ctx context.Context, userDB *sql.DB, glob, scope string, limit int) ([]SlotMemory, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []SlotMemory
	for _, m := range s.scopes(scope, userDB) {
		out = append(out, m.slotsByGlob(ctx, glob, limit)...)
	}
	return out, nil
}

// slotsByGlob runs the GLOB key query against this file.
func (m memDB) slotsByGlob(ctx context.Context, glob string, limit int) []SlotMemory {
	if m.db == nil {
		return nil
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT m.id, sl.key, sl.value, m.content
		FROM memory_slots sl JOIN memories m ON m.id = sl.memory_id
		WHERE sl.key GLOB ? AND (m.expires_at IS NULL OR m.expires_at > CURRENT_TIMESTAMP)
		ORDER BY m.created_at DESC LIMIT ?`, glob, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []SlotMemory
	for rows.Next() {
		var local int64
		var key, value, content string
		if rows.Scan(&local, &key, &value, &content) == nil {
			out = append(out, SlotMemory{ID: m.encode(local), Key: key, Value: value, Content: content})
		}
	}
	return out
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
				Key:          slot.Key,
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

// keysNearWithExample returns up to k distinct keys whose embeddings are closest
// to blob, each with a representative value (the nearest slot's value, capped).
func (m memDB) keysNearWithExample(ctx context.Context, blob any, k int) []slotextract.KeyExample {
	if m.db == nil || blob == nil {
		return nil
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT s.key, s.value FROM memory_slots_vec v JOIN memory_slots s ON s.id = v.rowid
		 WHERE v.embedding MATCH ? AND k = ? ORDER BY v.distance`, blob, k)
	if err != nil {
		return nil
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []slotextract.KeyExample
	for rows.Next() {
		var key, value string
		if rows.Scan(&key, &value) == nil && !seen[key] {
			seen[key] = true
			out = append(out, slotextract.KeyExample{Key: key, Example: capExample(value)})
		}
	}
	return out
}

// capExample trims a sample value to slotKeyExampleMax runes for the prompt.
func capExample(v string) string {
	v = strings.TrimSpace(v)
	if len(v) <= slotKeyExampleMax {
		return v
	}
	return strings.TrimSpace(v[:slotKeyExampleMax]) + "…"
}

// slotForMemory returns the slot row for one memory, if any.
func (m memDB) slotForMemory(ctx context.Context, memoryLocal int64) (slotextract.Slot, bool) {
	if m.db == nil {
		return slotextract.Slot{}, false
	}
	var slot slotextract.Slot
	err := m.db.QueryRowContext(ctx,
		`SELECT key, value FROM memory_slots WHERE memory_id = ? LIMIT 1`, memoryLocal).
		Scan(&slot.Key, &slot.Value)
	if err != nil {
		return slotextract.Slot{}, false
	}
	return slot, slot.Key != ""
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

// slotKeyExists reports whether any memory in the user or shared database
// already has a slot with the given normalized key.
func (s *Store) slotKeyExists(ctx context.Context, userDB *sql.DB, key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	for _, m := range []memDB{s.userMem(userDB), s.sharedMem()} {
		if len(m.slotsForKey(ctx, key)) > 0 {
			return true
		}
	}
	return false
}

// IsRedundant reports whether content is already represented in memory and
// should not be auto-stored again. turnWritten is verbatim content the
// assistant stored via memory_write this turn (checked before any LLM call).
func (s *Store) IsRedundant(ctx context.Context, userDB *sql.DB, userID int64, content string, turnWritten []string) bool {
	fact := strings.TrimSpace(content)
	if fact == "" {
		return true
	}
	for _, w := range turnWritten {
		if strings.EqualFold(strings.TrimSpace(w), fact) {
			return true
		}
	}
	if existing, err := s.Search(ctx, userDB, fact, userID, "", 5); err == nil {
		for _, r := range existing {
			if strings.EqualFold(strings.TrimSpace(r.Content), fact) {
				return true
			}
		}
	}
	if s.judge == nil {
		return false
	}
	blob, _ := s.embed(ctx, content)
	slot, ok := s.extractSlot(ctx, userDB, content, blob)
	if !ok {
		return false
	}
	return s.slotKeyExists(ctx, userDB, slot.Key)
}

type memorySlot struct {
	key   string
	value string
}

// slotsByMemoryLocals returns the slot (at most one per memory today) for each
// local memory row id in this database file.
func (m memDB) slotsByMemoryLocals(ctx context.Context, memoryLocals []int64) map[int64]memorySlot {
	if m.db == nil || len(memoryLocals) == 0 {
		return nil
	}
	placeholders := make([]string, len(memoryLocals))
	args := make([]any, len(memoryLocals))
	for i, id := range memoryLocals {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `SELECT memory_id, key, value FROM memory_slots WHERE memory_id IN (` +
		strings.Join(placeholders, ",") + `)`
	rows, err := m.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make(map[int64]memorySlot, len(memoryLocals))
	for rows.Next() {
		var local int64
		var sl memorySlot
		if rows.Scan(&local, &sl.key, &sl.value) == nil {
			out[local] = sl
		}
	}
	return out
}

// attachSlots fills SlotKey and SlotValue on each memory from memory_slots.
func (s *Store) attachSlots(ctx context.Context, userDB *sql.DB, mems []types.Memory) {
	if len(mems) == 0 {
		return
	}
	byScope := map[string][]int{}
	for i, mem := range mems {
		byScope[mem.Scope] = append(byScope[mem.Scope], i)
	}
	for scope, indices := range byScope {
		locals := make([]int64, 0, len(indices))
		for _, i := range indices {
			if _, local, ok := decodeID(mems[i].ID); ok {
				locals = append(locals, local)
			}
		}
		slots := s.memFor(scope, userDB).slotsByMemoryLocals(ctx, locals)
		for _, i := range indices {
			if _, local, ok := decodeID(mems[i].ID); ok {
				if sl, ok := slots[local]; ok {
					mems[i].SlotKey = sl.key
					mems[i].SlotValue = sl.value
				}
			}
		}
	}
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
