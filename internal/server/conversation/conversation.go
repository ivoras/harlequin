// Package conversation stores conversations and their messages. Conversations
// live in the owning user's per-user database, so methods take that handle and
// ownership is implicit (no user_id column or filter).
package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// Store manages conversations and messages. It is stateless: every method takes
// the user's database handle.
type Store struct{}

// NewStore constructs a conversation Store.
func NewStore() *Store {
	return &Store{}
}

// Create starts a new conversation in the user's database, optionally wearing a
// hat (empty for none). api/interface tie the session to its transport+medium
// (default REST/TUI when empty).
func (s *Store) Create(ctx context.Context, db *sql.DB, userID int64, title, hat, api, iface string) (*types.Conversation, error) {
	if title == "" {
		title = "New conversation"
	}
	if api == "" {
		api = types.APIREST
	}
	if iface == "" {
		iface = types.InterfaceTUI
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO conversations(title, hat, api, interface) VALUES (?, ?, ?, ?)`,
		title, nullableStr(hat), api, iface)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	now := time.Now()
	c := &types.Conversation{ID: id, UserID: userID, Title: title, API: api, Interface: iface, CreatedAt: now, UpdatedAt: now}
	if hat != "" {
		c.Hat = &hat
	}
	return c, nil
}

// SetHat sets (or clears, when hat is empty) the conversation's worn hat.
func (s *Store) SetHat(ctx context.Context, db *sql.DB, id int64, hat string) error {
	_, err := db.ExecContext(ctx, `UPDATE conversations SET hat = ? WHERE id = ?`, nullableStr(hat), id)
	return err
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// List returns the user's conversations, optionally filtered by a title substring.
func (s *Store) List(ctx context.Context, db *sql.DB, userID int64, q string) ([]types.Conversation, error) {
	query := `SELECT id, title, hat, api, interface, created_at, updated_at FROM conversations`
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
	var out []types.Conversation
	for rows.Next() {
		c := types.Conversation{UserID: userID}
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

// Get returns a conversation from the user's database.
func (s *Store) Get(ctx context.Context, db *sql.DB, id, userID int64) (*types.Conversation, error) {
	c := types.Conversation{UserID: userID}
	var hat sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT id, title, hat, api, interface, created_at, updated_at FROM conversations WHERE id = ?`, id).
		Scan(&c.ID, &c.Title, &hat, &c.API, &c.Interface, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if hat.Valid {
		c.Hat = &hat.String
	}
	return &c, nil
}

// Delete removes a conversation from the user's database.
func (s *Store) Delete(ctx context.Context, db *sql.DB, id, userID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, id)
	return err
}

// AddMessage appends a message to a conversation and bumps updated_at.
func (s *Store) AddMessage(ctx context.Context, db *sql.DB, conversationID int64, role, content string, toolCalls []types.ToolCall) (*types.Message, error) {
	return s.AddMessageFull(ctx, db, conversationID, role, content, toolCalls, "", "")
}

// AddMessageFull persists a message including a tool-result's toolCallID and name
// (empty for non-tool messages), so the conversation replays as a valid
// OpenAI-compatible sequence.
func (s *Store) AddMessageFull(ctx context.Context, db *sql.DB, conversationID int64, role, content string, toolCalls []types.ToolCall, toolCallID, name string) (*types.Message, error) {
	var tcJSON any
	if len(toolCalls) > 0 {
		b, err := json.Marshal(toolCalls)
		if err != nil {
			return nil, err
		}
		tcJSON = string(b)
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO messages(conversation_id, role, content, tool_calls, tool_call_id, name) VALUES (?, ?, ?, ?, ?, ?)`,
		conversationID, role, content, tcJSON, nullIfEmpty(toolCallID), nullIfEmpty(name))
	if err != nil {
		return nil, err
	}
	_, _ = db.ExecContext(ctx, `UPDATE conversations SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, conversationID)
	id, _ := res.LastInsertId()
	return &types.Message{
		ID: id, ConversationID: conversationID, Role: role, Content: content,
		ToolCalls: toolCalls, ToolCallID: toolCallID, Name: name, CreatedAt: time.Now(),
	}, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Messages returns the messages of a conversation in order.
func (s *Store) Messages(ctx context.Context, db *sql.DB, conversationID int64) ([]types.Message, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, conversation_id, role, content, tool_calls, tool_call_id, name, created_at
		 FROM messages WHERE conversation_id = ? ORDER BY id ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Message
	for rows.Next() {
		var m types.Message
		var tc, tcID, name sql.NullString
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &tc, &tcID, &name, &m.CreatedAt); err != nil {
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
