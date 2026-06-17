// Package notify stores server→user notifications in the owning user's per-user
// database. Like the session store it is stateless: every method takes the
// user's database handle and ownership is implicit.
package notify

import (
	"context"
	"database/sql"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// Status values for a notification.
const (
	StatusPending   = "pending"
	StatusDelivered = "delivered"
	StatusDismissed = "dismissed"
)

// Store manages notifications. Stateless; methods take the user's DB handle.
type Store struct{}

// NewStore constructs a notification Store.
func NewStore() *Store { return &Store{} }

// Create inserts a notification and returns its id.
func (s *Store) Create(ctx context.Context, db *sql.DB, n types.Notification) (int64, error) {
	auto := 0
	if n.AutoRun {
		auto = 1
	}
	var sessID any
	if n.SessionID != nil {
		sessID = *n.SessionID
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO notifications(kind, title, description, prompt, auto_run, status, session_id, target_interface)
		 VALUES (?,?,?,?,?,?,?,?)`,
		nullIfEmpty(n.Kind), n.Title, n.Description, nullIfEmpty(n.Prompt), auto, StatusPending, sessID, nullIfEmpty(n.Interface))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListPending returns the user's pending notifications, oldest first, that target
// the given interface (or are broadcast — target_interface NULL). Pass "" to get
// only broadcasts.
func (s *Store) ListPending(ctx context.Context, db *sql.DB, iface string) ([]types.Notification, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, kind, title, description, prompt, auto_run, status, session_id, target_interface
		 FROM notifications
		 WHERE status = ? AND (target_interface IS NULL OR target_interface = ?)
		 ORDER BY id`, StatusPending, iface)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Notification
	for rows.Next() {
		var (
			n               types.Notification
			kind, prompt    sql.NullString
			auto            int
			sessID          sql.NullInt64
			targetInterface sql.NullString
		)
		if err := rows.Scan(&n.ID, &kind, &n.Title, &n.Description, &prompt, &auto, &n.Status, &sessID, &targetInterface); err != nil {
			return nil, err
		}
		n.Kind, n.Prompt, n.AutoRun = kind.String, prompt.String, auto != 0
		if sessID.Valid {
			n.SessionID = &sessID.Int64
		}
		n.Interface = targetInterface.String
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkDelivered records that a notification was shown/handled by the client.
func (s *Store) MarkDelivered(ctx context.Context, db *sql.DB, id int64) error {
	return s.setStatus(ctx, db, id, StatusDelivered)
}

// Dismiss marks a notification dismissed.
func (s *Store) Dismiss(ctx context.Context, db *sql.DB, id int64) error {
	return s.setStatus(ctx, db, id, StatusDismissed)
}

func (s *Store) setStatus(ctx context.Context, db *sql.DB, id int64, status string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE notifications SET status = ?, delivered_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
	return err
}

// ExistsKind reports whether any notification of the given kind exists (any
// status), used to avoid re-queuing one-shot notifications like onboarding.
func (s *Store) ExistsKind(ctx context.Context, db *sql.DB, kind string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE kind = ?`, kind).Scan(&n)
	return n > 0, err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
