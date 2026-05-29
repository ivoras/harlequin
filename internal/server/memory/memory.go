// Package memory stores per-user and shared memories and provides hybrid
// (FTS5 + vector) search fused with Reciprocal Rank Fusion.
package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const rrfK = 60.0

// ErrNotFound is returned when a memory does not exist or is not visible to the caller.
var ErrNotFound = errors.New("memory not found")

// Store manages memories.
type Store struct {
	db       *sql.DB
	embedder embed.Embedder
	judge    llm.Provider
	conflictCandidates int
}

// NewStore constructs a memory Store.
func NewStore(db *sql.DB, embedder embed.Embedder) *Store {
	return &Store{db: db, embedder: embedder}
}

// Add inserts a memory and indexes it for FTS and vector search. Conflict
// detection (if a judge is configured) runs asynchronously in the background.
func (s *Store) Add(ctx context.Context, m types.CreateMemoryRequest, userID int64) (*types.Memory, error) {
	mem, err := s.add(ctx, m, userID)
	if err != nil {
		return nil, err
	}
	s.checkConflictsAsync(userID, mem.ID, m.Content)
	return mem, nil
}

// AddWithConflicts inserts a memory and synchronously detects conflicts with
// existing memories, returning any flagged hits so the caller can surface them
// to the user in-flow. If no judge is configured, hits is nil.
func (s *Store) AddWithConflicts(ctx context.Context, m types.CreateMemoryRequest, userID int64) (*types.Memory, []ConflictHit, error) {
	mem, err := s.add(ctx, m, userID)
	if err != nil {
		return nil, nil, err
	}
	hits, err := s.detectConflicts(ctx, userID, mem.ID, m.Content)
	if err != nil {
		// Detection failure must not fail the write; the memory is already stored.
		return mem, nil, nil
	}
	return mem, hits, nil
}

// add inserts a memory and indexes it, without triggering conflict detection.
func (s *Store) add(ctx context.Context, m types.CreateMemoryRequest, userID int64) (*types.Memory, error) {
	scope := m.Scope
	if scope == "" {
		scope = "user"
	}
	source := m.Source
	if source == "" {
		source = "manual"
	}

	var uid any
	if scope == "user" {
		uid = userID
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO memories(scope, user_id, content, source, expires_at) VALUES (?, ?, ?, ?, ?)`,
		scope, uid, m.Content, source, nullableTime(m.ExpiresAt))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()

	if _, err := tx.ExecContext(ctx, `INSERT INTO memories_fts(rowid, content) VALUES (?, ?)`, id, m.Content); err != nil {
		return nil, err
	}

	// Embed and store the vector.
	vecs, err := s.embedder.Embed(ctx, []string{m.Content})
	if err != nil {
		return nil, fmt.Errorf("embed memory: %w", err)
	}
	if len(vecs) == 1 {
		blob, err := sqlite_vec.SerializeFloat32(vecs[0])
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memories_vec(rowid, embedding) VALUES (?, ?)`, id, blob); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	mem, err := s.Get(ctx, id, userID)
	if err != nil {
		return &types.Memory{
			ID: id, Scope: scope, Content: m.Content, Source: source,
			ExpiresAt: m.ExpiresAt, CreatedAt: time.Now().UTC(),
		}, nil
	}
	return mem, nil
}

// Search returns memories matching the query for the given user/scope, using
// hybrid FTS + vector search fused with RRF.
func (s *Store) Search(ctx context.Context, query string, userID int64, scope string, limit int) ([]types.SearchResult, error) {
	if limit <= 0 {
		limit = 8
	}

	// Visibility filter: a user sees their own 'user' memories and all 'shared'.
	// visArg is appended only when the clause has a userID placeholder.
	visible := `(m.scope = 'shared' OR (m.scope = 'user' AND m.user_id = ?))`
	visArgs := []any{userID}
	switch scope {
	case "user":
		visible = `(m.scope = 'user' AND m.user_id = ?)`
		visArgs = []any{userID}
	case "shared":
		visible = `(m.scope = 'shared')`
		visArgs = nil
	}
	notExpired := `(m.expires_at IS NULL OR m.expires_at > CURRENT_TIMESTAMP)`

	ranks := map[int64]float64{}
	contents := map[int64]string{}

	// FTS leg: placeholders are query, [visArgs...], limit.
	ftsSQL := fmt.Sprintf(`SELECT m.id, m.content
		FROM memories_fts f JOIN memories m ON m.id = f.rowid
		WHERE memories_fts MATCH ? AND %s AND %s
		ORDER BY f.rank LIMIT ?`, visible, notExpired)
	ftsArgs := append([]any{query}, visArgs...)
	ftsArgs = append(ftsArgs, limit*4)
	if err := s.collect(ctx, ftsSQL, ftsArgs, ranks, contents); err != nil {
		// FTS MATCH can error on odd queries; ignore and rely on vector leg.
		ranks = map[int64]float64{}
	}

	// Vector leg: placeholders are blob, k, [visArgs...].
	if vecs, err := s.embedder.Embed(ctx, []string{query}); err == nil && len(vecs) == 1 {
		if blob, err := sqlite_vec.SerializeFloat32(vecs[0]); err == nil {
			vecSQL := fmt.Sprintf(`SELECT m.id, m.content
				FROM memories_vec v JOIN memories m ON m.id = v.rowid
				WHERE v.embedding MATCH ? AND k = ? AND %s AND %s
				ORDER BY v.distance`, visible, notExpired)
			vecArgs := append([]any{blob, limit * 4}, visArgs...)
			_ = s.collect(ctx, vecSQL, vecArgs, ranks, contents)
		}
	}

	return topN(ranks, contents, limit), nil
}

// collect runs a ranked query and folds 1/(k+rank) RRF scores into ranks.
func (s *Store) collect(ctx context.Context, query string, args []any, ranks map[int64]float64, contents map[int64]string) error {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	rank := 0
	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			return err
		}
		rank++
		ranks[id] += 1.0 / (rrfK + float64(rank))
		contents[id] = content
	}
	return rows.Err()
}

func topN(ranks map[int64]float64, contents map[int64]string, limit int) []types.SearchResult {
	out := make([]types.SearchResult, 0, len(ranks))
	for id, score := range ranks {
		out = append(out, types.SearchResult{ID: id, Content: contents[id], Score: score})
	}
	// Simple selection sort by score desc (lists are small).
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

// List returns memories visible to the user (newest first).
func (s *Store) List(ctx context.Context, userID int64, scope string, limit int) ([]types.Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id, scope, user_id, content, source, pinned, expires_at, created_at
		FROM memories
		WHERE (scope = 'shared' OR (scope = 'user' AND user_id = ?))`
	args := []any{userID}
	if scope == "user" || scope == "shared" {
		q += ` AND scope = ?`
		args = append(args, scope)
	}
	q += ` ORDER BY pinned DESC, created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

type memoryScanner interface {
	Scan(dest ...any) error
}

func scanMemory(row memoryScanner) (*types.Memory, error) {
	var m types.Memory
	var uid sql.NullInt64
	var exp sql.NullTime
	var pinned int
	if err := row.Scan(&m.ID, &m.Scope, &uid, &m.Content, &m.Source, &pinned, &exp, &m.CreatedAt); err != nil {
		return nil, err
	}
	if uid.Valid {
		m.UserID = &uid.Int64
	}
	if exp.Valid {
		m.ExpiresAt = &exp.Time
	}
	m.Pinned = pinned != 0
	return &m, nil
}

// SetPinned updates the pinned flag for a memory owned by/visible to the user.
func (s *Store) SetPinned(ctx context.Context, id, userID int64, pinned bool) error {
	v := 0
	if pinned {
		v = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE memories SET pinned = ? WHERE id = ? AND (user_id = ? OR scope = 'shared')`, v, id, userID)
	return err
}

// Get returns a memory visible to the user (their user-scoped rows or shared).
func (s *Store) Get(ctx context.Context, id, userID int64) (*types.Memory, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, scope, user_id, content, source, pinned, expires_at, created_at
		FROM memories
		WHERE id = ? AND (scope = 'shared' OR (scope = 'user' AND user_id = ?))`,
		id, userID)
	m, err := scanMemory(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return m, nil
}

// Delete removes a memory and its index rows. Users may delete their own
// user-scoped memories; admins may also delete shared memories.
func (s *Store) Delete(ctx context.Context, id, userID int64, asAdmin bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var res sql.Result
	if asAdmin {
		res, err = tx.ExecContext(ctx, `
			DELETE FROM memories WHERE id = ? AND (
				(scope = 'user' AND user_id = ?) OR scope = 'shared'
			)`, id, userID)
	} else {
		res, err = tx.ExecContext(ctx,
			`DELETE FROM memories WHERE id = ? AND scope = 'user' AND user_id = ?`, id, userID)
	}
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	_, _ = tx.ExecContext(ctx, `DELETE FROM memories_fts WHERE rowid = ?`, id)
	_, _ = tx.ExecContext(ctx, `DELETE FROM memories_vec WHERE rowid = ?`, id)
	return tx.Commit()
}

// SweepExpired deletes expired, unpinned memories. Returns the number removed.
func (s *Store) SweepExpired(ctx context.Context) (int64, error) {
	rows, err := s.db.QueryContext(ctx,
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
		_, _ = s.db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id)
		_, _ = s.db.ExecContext(ctx, `DELETE FROM memories_fts WHERE rowid = ?`, id)
		_, _ = s.db.ExecContext(ctx, `DELETE FROM memories_vec WHERE rowid = ?`, id)
	}
	return int64(len(ids)), nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}
