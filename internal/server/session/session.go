// Package session stores sessions and their messages. Sessions
// live in the owning user's per-user database, so methods take that handle and
// ownership is implicit (no user_id column or filter).
package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// Store manages sessions and messages. It is stateless: every method takes
// the user's database handle.
type Store struct{}

// NewStore constructs a session Store.
func NewStore() *Store {
	return &Store{}
}

// Create starts a new session in the user's database, optionally wearing a
// hat (empty for none). api/interface tie the session to its transport+medium
// (default REST/TUI when empty).
func (s *Store) Create(ctx context.Context, db *sql.DB, userID int64, title, hat, api, iface string) (*types.Session, error) {
	if title == "" {
		title = "New session"
	}
	if api == "" {
		api = types.APIREST
	}
	if iface == "" {
		iface = types.InterfaceTUI
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO sessions(title, hat, api, interface) VALUES (?, ?, ?, ?)`,
		title, nullableStr(hat), api, iface)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	now := time.Now()
	c := &types.Session{ID: id, UserID: userID, Title: title, API: api, Interface: iface, CreatedAt: now, UpdatedAt: now}
	if hat != "" {
		c.Hat = &hat
	}
	return c, nil
}

// SetHat sets (or clears, when hat is empty) the session's worn hat.
func (s *Store) SetHat(ctx context.Context, db *sql.DB, id int64, hat string) error {
	_, err := db.ExecContext(ctx, `UPDATE sessions SET hat = ? WHERE id = ?`, nullableStr(hat), id)
	return err
}

// SetTitle replaces a session's title (used by the auto-titler). It does not
// bump updated_at, so a system-generated title doesn't reorder the recents list.
func (s *Store) SetTitle(ctx context.Context, db *sql.DB, id int64, title string) error {
	_, err := db.ExecContext(ctx, `UPDATE sessions SET title = ? WHERE id = ?`, title, id)
	return err
}

// Rename replaces a session's title on the user's explicit request. Unlike
// SetTitle it bumps updated_at, so a deliberately renamed session surfaces at
// the top of the recents list.
func (s *Store) Rename(ctx context.Context, db *sql.DB, id int64, title string) error {
	_, err := db.ExecContext(ctx, `UPDATE sessions SET title = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, title, id)
	return err
}

// UntitledWithUserMessage returns sessions still carrying a generic/empty
// title that have at least one user message — the candidates for auto-titling.
// Internal (Cron) sessions are excluded.
func (s *Store) UntitledWithUserMessage(ctx context.Context, db *sql.DB) ([]types.Session, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, title, interface FROM sessions
		 WHERE (title = '' OR title = 'Session' OR title = 'New session' OR title = 'New conversation')
		   AND interface <> 'Cron'
		   AND EXISTS (SELECT 1 FROM messages m WHERE m.session_id = sessions.id AND m.role = 'user')
		 ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Session
	for rows.Next() {
		var c types.Session
		if err := rows.Scan(&c.ID, &c.Title, &c.Interface); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// List returns the user's sessions, optionally filtered by a title substring.
func (s *Store) List(ctx context.Context, db *sql.DB, userID int64, q string) ([]types.Session, error) {
	query := `SELECT id, title, hat, api, interface, created_at, updated_at FROM sessions`
	var args []any
	if q != "" {
		query += ` WHERE title LIKE ?`
		args = append(args, "%"+q+"%")
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Session
	for rows.Next() {
		c := types.Session{UserID: userID}
		var hat sql.NullString
		if err := rows.Scan(&c.ID, &c.Title, &hat, &c.API, &c.Interface, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if hat.Valid {
			c.Hat = &hat.String
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns a session from the user's database.
func (s *Store) Get(ctx context.Context, db *sql.DB, id, userID int64) (*types.Session, error) {
	c := types.Session{UserID: userID}
	var hat sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT id, title, hat, api, interface, created_at, updated_at FROM sessions WHERE id = ?`, id).
		Scan(&c.ID, &c.Title, &hat, &c.API, &c.Interface, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if hat.Valid {
		c.Hat = &hat.String
	}
	return &c, nil
}

// Delete removes a session from the user's database.
func (s *Store) Delete(ctx context.Context, db *sql.DB, id, userID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// ClearMessages deletes all of a session's messages (a "/clear": the session,
// its title, and its hat survive; the next turn starts with a fresh context).
func (s *Store) ClearMessages(ctx context.Context, db *sql.DB, sessionID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM messages WHERE session_id = ?`, sessionID)
	return err
}

// AddMessage appends a message to a session and bumps updated_at.
func (s *Store) AddMessage(ctx context.Context, db *sql.DB, sessionID int64, role, content string, toolCalls []types.ToolCall) (*types.Message, error) {
	return s.AddMessageFull(ctx, db, sessionID, role, content, toolCalls, "", "")
}

// AddMessageFull persists a message including a tool-result's toolCallID and name
// (empty for non-tool messages), so the session replays as a valid
// OpenAI-compatible sequence.
func (s *Store) AddMessageFull(ctx context.Context, db *sql.DB, sessionID int64, role, content string, toolCalls []types.ToolCall, toolCallID, name string) (*types.Message, error) {
	var tcJSON any
	if len(toolCalls) > 0 {
		b, err := json.Marshal(toolCalls)
		if err != nil {
			return nil, err
		}
		tcJSON = string(b)
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO messages(session_id, role, content, tool_calls, tool_call_id, name) VALUES (?, ?, ?, ?, ?, ?)`,
		sessionID, role, content, tcJSON, nullIfEmpty(toolCallID), nullIfEmpty(name))
	if err != nil {
		return nil, err
	}
	_, _ = db.ExecContext(ctx, `UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, sessionID)
	id, _ := res.LastInsertId()
	return &types.Message{
		ID: id, SessionID: sessionID, Role: role, Content: content,
		ToolCalls: toolCalls, ToolCallID: toolCallID, Name: name, CreatedAt: time.Now(),
	}, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Messages returns the messages of a session in order.
func (s *Store) Messages(ctx context.Context, db *sql.DB, sessionID int64) ([]types.Message, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, session_id, role, content, tool_calls, tool_call_id, name, created_at
		 FROM messages WHERE session_id = ? ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Message
	for rows.Next() {
		var m types.Message
		var tc, tcID, name sql.NullString
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &tc, &tcID, &name, &m.CreatedAt); err != nil {
			return nil, err
		}
		if tc.Valid && tc.String != "" {
			_ = json.Unmarshal([]byte(tc.String), &m.ToolCalls)
		}
		m.ToolCallID = tcID.String
		m.Name = name.String
		out = append(out, m)
	}
	return out, rows.Err()
}

// LastMessageID returns the highest message id in a session (0 if none). The
// session hub captures this just before a turn starts as the watermark between
// durable history and the in-flight turn's replayable event buffer.
func (s *Store) LastMessageID(ctx context.Context, db *sql.DB, sessionID int64) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM messages WHERE session_id = ?`, sessionID).Scan(&id)
	return id, err
}
