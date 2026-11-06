// Package audit provides an append-only audit log for coarse security events.
// Entries live in the acting user's per-user database, so methods take that
// handle and the owning user is implicit (no user_id column).
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

// Filter narrows a search/stats query. Zero values mean "no constraint".
type Filter struct {
	Q        string   // case-insensitive substring of action, target or detail
	Actions  []string // exact action names (any of)
	From, To time.Time
	TZ       int // viewer's UTC offset in minutes, for day bucketing in Stats
}

// ts matches CURRENT_TIMESTAMP's "YYYY-MM-DD HH:MM:SS" so string comparison orders correctly.
const ts = "2006-01-02 15:04:05"

func (f Filter) where() (string, []any) {
	var c []string
	var a []any
	if q := strings.TrimSpace(f.Q); q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%"
		c = append(c, `(action LIKE ? ESCAPE '\' OR target LIKE ? ESCAPE '\' OR IFNULL(detail,'') LIKE ? ESCAPE '\')`)
		a = append(a, like, like, like)
	}
	if len(f.Actions) > 0 {
		c = append(c, "action IN (?"+strings.Repeat(",?", len(f.Actions)-1)+")")
		for _, x := range f.Actions {
			a = append(a, x)
		}
	}
	if !f.From.IsZero() {
		c = append(c, "created_at >= ?")
		a = append(a, f.From.UTC().Format(ts))
	}
	if !f.To.IsZero() {
		c = append(c, "created_at < ?")
		a = append(a, f.To.UTC().Format(ts))
	}
	if len(c) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(c, " AND "), a
}

// Search is List with filters applied.
func (s *Store) Search(ctx context.Context, db *sql.DB, userID int64, f Filter, limit int) ([]types.AuditEntry, error) {
	w, a := f.where()
	rows, err := db.QueryContext(ctx,
		`SELECT id, action, target, detail, created_at FROM audit_log`+w+` ORDER BY id DESC LIMIT ?`,
		append(a, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.AuditEntry
	for rows.Next() {
		e := types.AuditEntry{UserID: &userID}
		var detail sql.NullString
		if err := rows.Scan(&e.ID, &e.Action, &e.Target, &detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Detail = detail.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// Stats summarises one user's matching entries: totals per action and per day
// (days in the viewer's local time).
func (s *Store) Stats(ctx context.Context, db *sql.DB, f Filter) (actions, days map[string]int, err error) {
	w, a := f.where()
	actions, days = map[string]int{}, map[string]int{}
	group := func(expr string, args []any, into map[string]int) error {
		rows, err := db.QueryContext(ctx, `SELECT `+expr+`, COUNT(*) FROM audit_log`+w+` GROUP BY 1`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				return err
			}
			into[k] = n
		}
		return rows.Err()
	}
	if err = group("action", a, actions); err != nil {
		return
	}
	// The modifier is a bound parameter, so it precedes the WHERE args.
	err = group("date(created_at, ?)", append([]any{fmt.Sprintf("%+d minutes", f.TZ)}, a...), days)
	return
}
