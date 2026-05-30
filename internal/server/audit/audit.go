// Package audit provides an append-only audit log for coarse security events.
// Entries live in the acting user's per-user database, so methods take that
// handle and the owning user is implicit (no user_id column).
package audit

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// Store writes and queries audit entries. It is stateless: every method takes
// the user's database handle.
type Store struct{}

// NewStore constructs an audit Store.
func NewStore() *Store {
	return &Store{}
}

// Log appends an audit entry to the user's database. detail may be any
// JSON-serializable value or nil.
func (s *Store) Log(ctx context.Context, db *sql.DB, action, target string, detail any) {
	var detailJSON any
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			detailJSON = string(b)
		}
	}
	// Best-effort: audit failures must not break the request.
	_, _ = db.ExecContext(ctx,
		`INSERT INTO audit_log(action, target, detail) VALUES (?, ?, ?)`,
		action, target, detailJSON)
}

// List returns recent audit entries from one user's database, tagging each with
// the given user id. Callers aggregate across users via storage.EachUser.
func (s *Store) List(ctx context.Context, db *sql.DB, userID int64, limit int) ([]types.AuditEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, action, target, detail, created_at FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.AuditEntry
	for rows.Next() {
		e := types.AuditEntry{}
		uid := userID
		e.UserID = &uid
		var detail sql.NullString
		if err := rows.Scan(&e.ID, &e.Action, &e.Target, &detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		if detail.Valid {
			e.Detail = detail.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
