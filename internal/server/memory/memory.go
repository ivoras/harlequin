// Package memory stores per-user and shared memories and provides hybrid
// (FTS5 + vector) search fused with Reciprocal Rank Fusion.
//
// Memories live in two database files with an identical `memories` schema: a
// user's own memories in their per-user database, and org memories in the
// shared database. Because both files are structurally identical, every
// per-file operation is implemented once on the memDB type (a handle plus its
// scope label); the Store just selects which memDB to use. A memory's public id
// is composite ("u.<localid>" / "s.<localid>") so a user's fused view of both
// files is unambiguous.
package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const rrfK = 60.0

// ErrNotFound is returned when a memory does not exist or is not visible to the caller.
var ErrNotFound = errors.New("memory not found")

// Store manages memories across the shared database (held) and per-user
// databases (passed in per call).
type Store struct {
	shared             *sql.DB
	embedder           embed.Embedder
	judge              llm.Provider
	conflictCandidates int
	slotSearchWeight   float64
	searchMaxDist      float64 // cosine-distance cutoff for vector/slot legs; 0 = no cutoff
}

// NewStore constructs a memory Store bound to the shared database.
func NewStore(shared *sql.DB, embedder embed.Embedder) *Store {
	return &Store{shared: shared, embedder: embedder}
}

// SetSlotSearchWeight sets the RRF weight of the slot-key leg used by Search
// (0 disables it). See docs/memory_experiment_key_slots.md.
func (s *Store) SetSlotSearchWeight(w float64) { s.slotSearchWeight = w }

// SetSearchMaxDistance sets the cosine-distance cutoff applied to the vector and
// slot-key search legs (candidates farther than this are dropped before RRF).
// 0 disables the cutoff. Requires the vec0 tables to use distance_metric=cosine.
func (s *Store) SetSearchMaxDistance(d float64) { s.searchMaxDist = d }

// memDB is one memories-bearing database file together with its scope label.
// All operations on a single file (insert, search, list, get, delete, conflict
// bookkeeping) are methods here, so the user and shared databases share one
// implementation.
type memDB struct {
	db    *sql.DB
	scope string
}

func (s *Store) userMem(userDB *sql.DB) memDB { return memDB{db: userDB, scope: scopeUser} }
func (s *Store) sharedMem() memDB             { return memDB{db: s.shared, scope: scopeShared} }

// memFor selects the memDB that holds a given scope's memories.
func (s *Store) memFor(scope string, userDB *sql.DB) memDB {
	if scope == scopeShared {
		return s.sharedMem()
	}
	return s.userMem(userDB)
}

// encode builds the composite id for a local rowid in this file.
func (m memDB) encode(local int64) string { return encodeID(m.scope, local) }

// Add inserts a memory into the scope's database and indexes it.
func (s *Store) Add(ctx context.Context, userDB *sql.DB, m types.CreateMemoryRequest, userID int64) (*types.Memory, error) {
	return s.add(ctx, userDB, m, userID)
}

// AddWithConflicts inserts a memory and synchronously detects conflicts with
// existing memories (across the user and shared databases), returning any
// flagged hits so the caller can surface them in-flow. If no judge is
// configured, hits is nil.
func (s *Store) AddWithConflicts(ctx context.Context, userDB *sql.DB, m types.CreateMemoryRequest, userID int64) (*types.Memory, []ConflictHit, error) {
	mem, err := s.add(ctx, userDB, m, userID)
	if err != nil {
		return nil, nil, err
	}
	hits, err := s.detectConflicts(ctx, userDB, userID, mem.ID, m.Content)
	if err != nil {
		// Detection failure must not fail the write; the memory is already stored.
		return mem, nil, nil
	}
	return mem, hits, nil
}

// ChangeWithConflicts replaces a memory's content in place (same composite id and
// scope), re-indexes FTS/vector/slots, clears stale conflict rows for that id,
// and runs conflict detection on the new text.
func (s *Store) ChangeWithConflicts(ctx context.Context, userDB *sql.DB, id, content string, userID int64, canManageShared bool) (*types.Memory, []ConflictHit, error) {
	scope, local, ok := decodeID(id)
	if !ok {
		return nil, nil, ErrNotFound
	}
	if scope == scopeShared && !canManageShared {
		return nil, nil, ErrNotFound
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil, fmt.Errorf("empty content")
	}

	mem, err := s.Get(ctx, userDB, id, userID)
	if err != nil {
		return nil, nil, err
	}

	blob, err := s.embed(ctx, content)
	if err != nil {
		return nil, nil, fmt.Errorf("embed memory content: %w", err)
	}
	m := s.memFor(scope, userDB)
	updated, err := m.updateContent(ctx, local, content, blob)
	if err != nil {
		return nil, nil, err
	}
	if !updated {
		return nil, nil, ErrNotFound
	}

	s.deleteConflictsFor(ctx, userDB, id)
	hits, err := s.detectConflicts(ctx, userDB, userID, id, content)
	if err != nil {
		return mem, nil, nil
	}
	mem.Content = content
	filled := []types.Memory{*mem}
	s.attachSlots(ctx, userDB, filled)
	*mem = filled[0]
	return mem, hits, nil
}

func (s *Store) add(ctx context.Context, userDB *sql.DB, m types.CreateMemoryRequest, userID int64) (*types.Memory, error) {
	scope := m.Scope
	if scope == "" {
		scope = scopeUser
	}
	source := m.Source
	if source == "" {
		source = "manual"
	}

	blob, err := s.embed(ctx, m.Content)
	if err != nil {
		return nil, fmt.Errorf("embed memory content: %w", err)
	}

	id, err := s.memFor(scope, userDB).insert(ctx, m.Content, source, nullableTime(m.ExpiresAt), blob)
	if err != nil {
		return nil, fmt.Errorf("insert memory row: %w", err)
	}

	mem := &types.Memory{
		ID: encodeID(scope, id), Scope: scope, Content: m.Content, Source: source,
		ExpiresAt: m.ExpiresAt, CreatedAt: time.Now().UTC(),
	}
	if scope == scopeUser {
		uid := userID
		mem.UserID = &uid
	}
	_, _ = s.indexSlot(ctx, userDB, mem.ID, m.Content, blob)
	return mem, nil
}

// insert writes a memory plus its FTS and vector index rows atomically.
func (m memDB) insert(ctx context.Context, content, source string, expires, blob any) (int64, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO memories(content, source, expires_at) VALUES (?, ?, ?)`, content, source, expires)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO memories_fts(rowid, content) VALUES (?, ?)`, id, content); err != nil {
		return 0, err
	}
	if blob != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memories_vec(rowid, embedding) VALUES (?, ?)`, id, blob); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// updateContent replaces a memory row's text and rebuilds its FTS, vector, and
// slot index rows atomically.
func (m memDB) updateContent(ctx context.Context, local int64, content string, blob any) (bool, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `UPDATE memories SET content = ? WHERE id = ?`, content, local)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memories_fts WHERE rowid = ?`, local); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memories_fts(rowid, content) VALUES (?, ?)`, local, content); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memories_vec WHERE rowid = ?`, local); err != nil {
		return false, err
	}
	if blob != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memories_vec(rowid, embedding) VALUES (?, ?)`, local, blob); err != nil {
			return false, err
		}
	}
	if err := deleteSlots(ctx, tx, local); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// embed returns the serialized embedding for text as a DOCUMENT (stored memory
// content / slot keys), or nil if embedding fails.
func (s *Store) embed(ctx context.Context, text string) (any, error) {
	return s.serialize(s.embedder.Embed(ctx, []string{text}))
}

// embedQuery is like embed but treats text as a SEARCH QUERY, so asymmetric
// models apply their query prompt prefix. Used on the memory search path only.
func (s *Store) embedQuery(ctx context.Context, text string) (any, error) {
	return s.serialize(s.embedder.EmbedQuery(ctx, []string{text}))
}

func (s *Store) serialize(vecs [][]float32, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, nil
	}
	return sqlite_vec.SerializeFloat32(vecs[0])
}

// ReindexMemoryVectors re-embeds every memory's content and rewrites its
// memories_vec row. Use after recreating the vec0 table (e.g. a metric change).
// Returns the number of memories reindexed.
func (s *Store) ReindexMemoryVectors(ctx context.Context, db *sql.DB) (int, error) {
	if db == nil {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT id, content FROM memories`)
	if err != nil {
		return 0, err
	}
	type mem struct {
		id      int64
		content string
	}
	var all []mem
	for rows.Next() {
		var m mem
		if err := rows.Scan(&m.id, &m.content); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, m := range all {
		blob, err := s.embed(ctx, m.content)
		if err != nil || blob == nil {
			continue
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO memories_vec(rowid, embedding) VALUES (?, ?)`, m.id, blob); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Find runs a hybrid search and returns the matching memories as full records,
// ranked best-first, so callers can render them like a memory listing.
func (s *Store) Find(ctx context.Context, userDB *sql.DB, query string, userID int64, limit int) ([]types.Memory, error) {
	res, err := s.Search(ctx, userDB, query, userID, "", limit)
	if err != nil {
		return nil, err
	}
	out := make([]types.Memory, 0, len(res))
	for _, r := range res {
		m, err := s.Get(ctx, userDB, r.ID, userID)
		if err != nil {
			continue
		}
		out = append(out, *m)
	}
	s.attachSlots(ctx, userDB, out)
	return out, nil
}

// Search returns memories matching the query, fusing the user and shared
// databases with hybrid FTS + vector search and RRF. scope ("user"|"shared"|"")
// narrows which databases are consulted.
func (s *Store) Search(ctx context.Context, userDB *sql.DB, query string, userID int64, scope string, limit int) ([]types.SearchResult, error) {
	return s.searchTuned(ctx, userDB, nil, query, userID, scope, limit, s.slotSearchWeight)
}

// SearchFused searches the user + shared + project memories together (used in a
// project session), so a member's results draw on all three scopes.
func (s *Store) SearchFused(ctx context.Context, userDB, projDB *sql.DB, query string, userID int64, limit int) ([]types.SearchResult, error) {
	return s.searchTuned(ctx, userDB, projDB, query, userID, "", limit, s.slotSearchWeight)
}

// SearchTuned is Search with an additional slot-key RRF leg, weighted by
// slotWeight (0 disables it, reproducing plain Search). It lets the slot-key
// embeddings contribute an attribute-match signal to ranking. Exposed for
// tuning and evaluation.
func (s *Store) SearchTuned(ctx context.Context, userDB *sql.DB, query string, userID int64, scope string, limit int, slotWeight float64) ([]types.SearchResult, error) {
	return s.searchTuned(ctx, userDB, nil, query, userID, scope, limit, slotWeight)
}

func (s *Store) searchTuned(ctx context.Context, userDB, projDB *sql.DB, query string, userID int64, scope string, limit int, slotWeight float64) ([]types.SearchResult, error) {
	if limit <= 0 {
		limit = 8
	}
	var blob any
	if b, err := s.embedQuery(ctx, query); err == nil {
		blob = b
	}

	ranks := map[string]float64{}
	contents := map[string]string{}
	for _, m := range s.scopesWith(scope, userDB, projDB) {
		m.search(ctx, query, blob, limit, slotWeight, s.searchMaxDist, ranks, contents)
	}
	out := topN(ranks, contents, limit)
	s.attachSlotsToResults(ctx, userDB, projDB, out)
	return out, nil
}

// attachSlotsToResults fills SlotKeys on search hits with every slot key the
// memory carries.
func (s *Store) attachSlotsToResults(ctx context.Context, userDB, projDB *sql.DB, results []types.SearchResult) {
	for i, r := range results {
		scope, local, ok := decodeID(r.ID)
		if !ok {
			continue
		}
		var m memDB
		if scope == scopeProject {
			if projDB == nil {
				continue
			}
			m = s.projectMem(projDB)
		} else {
			m = s.memFor(scope, userDB)
		}
		for _, slot := range m.slotsForMemory(ctx, local) {
			results[i].SlotKeys = append(results[i].SlotKeys, slot.Key)
		}
	}
}

// scopes returns the memDBs to consult for a scope filter.
func (s *Store) scopes(scope string, userDB *sql.DB) []memDB {
	return s.scopesWith(scope, userDB, nil)
}

// scopesWith is scopes plus the project memDB when projDB is set (project session).
func (s *Store) scopesWith(scope string, userDB, projDB *sql.DB) []memDB {
	var out []memDB
	if scope != scopeShared {
		out = append(out, s.userMem(userDB))
	}
	if scope != scopeUser {
		out = append(out, s.sharedMem())
	}
	if projDB != nil && (scope == "" || scope == scopeProject) {
		out = append(out, s.projectMem(projDB))
	}
	return out
}

// search runs the FTS + vector legs against this file, folding RRF scores keyed
// by composite id into ranks/contents.
func (m memDB) search(ctx context.Context, query string, blob any, limit int, slotWeight, maxDist float64, ranks map[string]float64, contents map[string]string) {
	if m.db == nil {
		return
	}
	notExpired := `(m.expires_at IS NULL OR m.expires_at > CURRENT_TIMESTAMP)`
	ftsSQL := `SELECT m.id, m.content
		FROM memories_fts f JOIN memories m ON m.id = f.rowid
		WHERE memories_fts MATCH ? AND ` + notExpired + `
		ORDER BY f.rank LIMIT ?`
	_ = m.collect(ctx, ftsSQL, []any{query, limit * 4}, 1.0, ranks, contents)

	if blob != nil {
		// Vector and slot legs are kNN with no inherent relevance floor, so a
		// cosine-distance cutoff (maxDist; vec0 distance is cosine) drops far,
		// irrelevant candidates before they earn an RRF score.
		vecSQL := `SELECT m.id, m.content, v.distance
			FROM memories_vec v JOIN memories m ON m.id = v.rowid
			WHERE v.embedding MATCH ? AND k = ? AND ` + notExpired + `
			ORDER BY v.distance`
		_ = m.collectVec(ctx, vecSQL, []any{blob, limit * 4}, 1.0, maxDist, ranks, contents)

		// Slot-key leg (optional): nearest slot keys by embedding, mapped back to
		// their memories, folded in with slotWeight. Surfaces attribute matches.
		if slotWeight > 0 {
			slotSQL := `SELECT m.id, m.content, v.distance
				FROM memory_slots_vec v
				JOIN memory_slots sl ON sl.id = v.rowid
				JOIN memories m ON m.id = sl.memory_id
				WHERE v.embedding MATCH ? AND k = ? AND ` + notExpired + `
				ORDER BY v.distance`
			_ = m.collectVec(ctx, slotSQL, []any{blob, limit * 4}, slotWeight, maxDist, ranks, contents)
		}
	}
}

// collect runs a ranked query and folds weight/(k+rank) RRF scores into ranks.
func (m memDB) collect(ctx context.Context, query string, args []any, weight float64, ranks map[string]float64, contents map[string]string) error {
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	rank := 0
	for rows.Next() {
		var local int64
		var content string
		if err := rows.Scan(&local, &content); err != nil {
			return err
		}
		rank++
		id := m.encode(local)
		ranks[id] += weight / (rrfK + float64(rank))
		contents[id] = content
	}
	return rows.Err()
}

// collectVec is collect for a leg that also yields a distance column: rows whose
// distance exceeds maxDist (when maxDist > 0) are skipped before scoring, and
// the rank used for RRF only advances for kept rows.
func (m memDB) collectVec(ctx context.Context, query string, args []any, weight, maxDist float64, ranks map[string]float64, contents map[string]string) error {
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	rank := 0
	for rows.Next() {
		var local int64
		var content string
		var distance float64
		if err := rows.Scan(&local, &content, &distance); err != nil {
			return err
		}
		if maxDist > 0 && distance > maxDist {
			continue
		}
		rank++
		id := m.encode(local)
		ranks[id] += weight / (rrfK + float64(rank))
		contents[id] = content
	}
	return rows.Err()
}

func topN(ranks map[string]float64, contents map[string]string, limit int) []types.SearchResult {
	out := make([]types.SearchResult, 0, len(ranks))
	for id, score := range ranks {
		out = append(out, types.SearchResult{ID: id, Content: contents[id], Score: score})
	}
	for i := 0; i < len(out); i++ {
		max := i
		for j := i + 1; j < len(out); j++ {
			if out[j].Score > out[max].Score {
				max = j
			}
		}
		out[i], out[max] = out[max], out[i]
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// List returns memories visible to the user (pinned/newest first), fusing the
// user and shared databases. scope narrows the result to one database.
func (s *Store) List(ctx context.Context, userDB *sql.DB, userID int64, scope string, limit int) ([]types.Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []types.Memory
	for _, m := range s.scopes(scope, userDB) {
		ms, err := m.list(ctx, userID, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, ms...)
	}
	// Pinned first, then newest, then trim to the requested limit.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	s.attachSlots(ctx, userDB, out)
	return out, nil
}

func (m memDB) list(ctx context.Context, userID int64, limit int) ([]types.Memory, error) {
	if m.db == nil {
		return nil, nil
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, content, source, pinned, expires_at, created_at
		 FROM memories ORDER BY pinned DESC, created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Memory
	for rows.Next() {
		mem, err := m.scan(rows, userID)
		if err != nil {
			return nil, err
		}
		out = append(out, *mem)
	}
	return out, rows.Err()
}

type memoryScanner interface {
	Scan(dest ...any) error
}

// scan reads a memories row (id, content, source, pinned, expires_at,
// created_at) and assigns the composite id + scope for this file.
func (m memDB) scan(row memoryScanner, userID int64) (*types.Memory, error) {
	var local int64
	var content, source string
	var pinned int
	var exp sql.NullTime
	var created time.Time
	if err := row.Scan(&local, &content, &source, &pinned, &exp, &created); err != nil {
		return nil, err
	}
	mem := &types.Memory{
		ID: m.encode(local), Scope: m.scope, Content: content,
		Source: source, Pinned: pinned != 0, CreatedAt: created,
	}
	if exp.Valid {
		mem.ExpiresAt = &exp.Time
	}
	if m.scope == scopeUser {
		uid := userID
		mem.UserID = &uid
	}
	return mem, nil
}

// SetPinned updates the pinned flag for a memory visible to the user.
func (s *Store) SetPinned(ctx context.Context, userDB *sql.DB, id string, userID int64, pinned bool) error {
	scope, local, ok := decodeID(id)
	if !ok {
		return ErrNotFound
	}
	v := 0
	if pinned {
		v = 1
	}
	_, err := s.memFor(scope, userDB).db.ExecContext(ctx,
		`UPDATE memories SET pinned = ? WHERE id = ?`, v, local)
	return err
}

// Get returns a memory visible to the user by composite id.
func (s *Store) Get(ctx context.Context, userDB *sql.DB, id string, userID int64) (*types.Memory, error) {
	scope, local, ok := decodeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	m := s.memFor(scope, userDB)
	row := m.db.QueryRowContext(ctx,
		`SELECT id, content, source, pinned, expires_at, created_at FROM memories WHERE id = ?`, local)
	mem, err := m.scan(row, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	filled := []types.Memory{*mem}
	s.attachSlots(ctx, userDB, filled)
	*mem = filled[0]
	return mem, nil
}

// Delete removes a memory and its index rows. Users may delete their own
// user-scoped memories; only callers with canManageShared (owner/admin) may
// delete shared memories. Dangling conflict rows referencing the memory are
// cleaned up (no cross-file FK).
func (s *Store) Delete(ctx context.Context, userDB *sql.DB, id string, userID int64, canManageShared bool) error {
	scope, local, ok := decodeID(id)
	if !ok {
		return ErrNotFound
	}
	if scope == scopeShared && !canManageShared {
		return ErrNotFound
	}
	found, err := s.memFor(scope, userDB).deleteMemory(ctx, local)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	s.deleteConflictsFor(ctx, userDB, id)
	return nil
}

// deleteMemory removes a memory row plus its index rows atomically, reporting
// whether a row existed.
func (m memDB) deleteMemory(ctx context.Context, local int64) (bool, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, local)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	_, _ = tx.ExecContext(ctx, `DELETE FROM memories_fts WHERE rowid = ?`, local)
	_, _ = tx.ExecContext(ctx, `DELETE FROM memories_vec WHERE rowid = ?`, local)
	_ = deleteSlots(ctx, tx, local)
	return true, tx.Commit()
}

// SweepExpiredDB deletes expired, unpinned memories from one database and
// returns the number removed. Callers sweep the shared database and each user
// database (see storage.EachUser).
func (s *Store) SweepExpiredDB(ctx context.Context, db *sql.DB) (int64, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id FROM memories WHERE pinned = 0 AND expires_at IS NOT NULL AND expires_at <= CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		_ = deleteSlots(ctx, db, id)
		_, _ = db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id)
		_, _ = db.ExecContext(ctx, `DELETE FROM memories_fts WHERE rowid = ?`, id)
		_, _ = db.ExecContext(ctx, `DELETE FROM memories_vec WHERE rowid = ?`, id)
	}
	return int64(len(ids)), nil
}

// SharedDB exposes the shared database handle for maintenance sweeps.
func (s *Store) SharedDB() *sql.DB { return s.shared }

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}
