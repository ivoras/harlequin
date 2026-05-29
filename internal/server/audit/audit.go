// Package audit provides an append-only audit log for coarse security events.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// Store writes and queries audit entries.
type Store struct {
	db *sql.DB
}

// NewStore constructs an audit Store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Log appends an audit entry. detail may be any JSON-serializable value or nil.
func (s *Store) Log(ctx context.Context, userID *int64, action, target string, detail any) {
	var detailJSON any
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			detailJSON = string(b)
		}
	}
	var uid any
	if userID != nil {
		uid = *userID
	}
	// Best-effort: audit failures must not break the request.
	_, _ = s.db.ExecContext(ctx,
		`INSERT INTO audit_log(user_id, action, target, detail) VALUES (?, ?, ?, ?)`,
		uid, action, target, detailJSON)
}

// List returns recent audit entries.
func (s *Store) List(ctx context.Context, limit int) ([]types.AuditEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, action, target, detail, created_at FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.AuditEntry
	for rows.Next() {
		var e types.AuditEntry
		var uid sql.NullInt64
		var detail sql.NullString
		if err := rows.Scan(&e.ID, &uid, &e.Action, &e.Target, &detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		if uid.Valid {
			e.UserID = &uid.Int64
		}
		if detail.Valid {
			e.Detail = detail.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
