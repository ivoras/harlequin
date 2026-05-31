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
}

// NewStore constructs a memory Store bound to the shared database.
func NewStore(shared *sql.DB, embedder embed.Embedder) *Store {
	return &Store{shared: shared, embedder: embedder}
}

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
		return nil, fmt.Errorf("embed memory: %w", err)
	}

	id, err := s.memFor(scope, userDB).insert(ctx, m.Content, source, nullableTime(m.ExpiresAt), blob)
	if err != nil {
		return nil, err
	}

	mem := &types.Memory{
		ID: encodeID(scope, id), Scope: scope, Content: m.Content, Source: source,
		ExpiresAt: m.ExpiresAt, CreatedAt: time.Now().UTC(),
	}
	if scope == scopeUser {
		uid := userID
		mem.UserID = &uid
	}
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

// embed returns the serialized embedding for text, or nil if embedding fails.
func (s *Store) embed(ctx context.Context, text string) (any, error) {
	vecs, err := s.embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, nil
	}
	return sqlite_vec.SerializeFloat32(vecs[0])
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
	return out, nil
}

// Search returns memories matching the query, fusing the user and shared
// databases with hybrid FTS + vector search and RRF. scope ("user"|"shared"|"")
// narrows which databases are consulted.
func (s *Store) Search(ctx context.Context, userDB *sql.DB, query string, userID int64, scope string, limit int) ([]types.SearchResult, error) {
	if limit <= 0 {
		limit = 8
	}
	var blob any
	if b, err := s.embed(ctx, query); err == nil {
		blob = b
	}

	ranks := map[string]float64{}
	contents := map[string]string{}
	for _, m := range s.scopes(scope, userDB) {
		m.search(ctx, query, blob, limit, ranks, contents)
	}
	return topN(ranks, contents, limit), nil
}

// scopes returns the memDBs to consult for a scope filter.
func (s *Store) scopes(scope string, userDB *sql.DB) []memDB {
	var out []memDB
	if scope != scopeShared {
		out = append(out, s.userMem(userDB))
	}
	if scope != scopeUser {
		out = append(out, s.sharedMem())
	}
	return out
}

// search runs the FTS + vector legs against this file, folding RRF scores keyed
// by composite id into ranks/contents.
func (m memDB) search(ctx context.Context, query string, blob any, limit int, ranks map[string]float64, contents map[string]string) {
	if m.db == nil {
		return
	}
	notExpired := `(m.expires_at IS NULL OR m.expires_at > CURRENT_TIMESTAMP)`
	ftsSQL := `SELECT m.id, m.content
		FROM memories_fts f JOIN memories m ON m.id = f.rowid
		WHERE memories_fts MATCH ? AND ` + notExpired + `
		ORDER BY f.rank LIMIT ?`
	_ = m.collect(ctx, ftsSQL, []any{query, limit * 4}, ranks, contents)

	if blob != nil {
		vecSQL := `SELECT m.id, m.content
			FROM memories_vec v JOIN memories m ON m.id = v.rowid
			WHERE v.embedding MATCH ? AND k = ? AND ` + notExpired + `
			ORDER BY v.distance`
		_ = m.collect(ctx, vecSQL, []any{blob, limit * 4}, ranks, contents)
	}
}

// collect runs a ranked query and folds 1/(k+rank) RRF scores into ranks.
func (m memDB) collect(ctx context.Context, query string, args []any, ranks map[string]float64, contents map[string]string) error {
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
		ranks[id] += 1.0 / (rrfK + float64(rank))
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
