// Package conversation stores conversations and their messages.
package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// Store manages conversations and messages.
type Store struct {
	db *sql.DB
}

// NewStore constructs a conversation Store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Create starts a new conversation.
func (s *Store) Create(ctx context.Context, userID int64, title string) (*types.Conversation, error) {
	if title == "" {
		title = "New conversation"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO conversations(user_id, title) VALUES (?, ?)`, userID, title)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	now := time.Now()
	return &types.Conversation{ID: id, UserID: userID, Title: title, CreatedAt: now, UpdatedAt: now}, nil
}

// List returns the user's conversations, optionally filtered by a title substring.
func (s *Store) List(ctx context.Context, userID int64, q string) ([]types.Conversation, error) {
	query := `SELECT id, user_id, title, created_at, updated_at FROM conversations WHERE user_id = ?`
	args := []any{userID}
	if q != "" {
		query += ` AND title LIKE ?`
		args = append(args, "%"+q+"%")
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Conversation
	for rows.Next() {
		var c types.Conversation
		if err := rows.Scan(&c.ID, &c.UserID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns a conversation if it belongs to the user.
func (s *Store) Get(ctx context.Context, id, userID int64) (*types.Conversation, error) {
	var c types.Conversation
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, title, created_at, updated_at FROM conversations WHERE id = ? AND user_id = ?`,
		id, userID).Scan(&c.ID, &c.UserID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// Delete removes a conversation owned by the user.
func (s *Store) Delete(ctx context.Context, id, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM conversations WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// AddMessage appends a message to a conversation and bumps updated_at.
func (s *Store) AddMessage(ctx context.Context, conversationID int64, role, content string, toolCalls []types.ToolCall) (*types.Message, error) {
	var tcJSON any
	if len(toolCalls) > 0 {
		b, err := json.Marshal(toolCalls)
		if err != nil {
			return nil, err
		}
		tcJSON = string(b)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages(conversation_id, role, content, tool_calls) VALUES (?, ?, ?, ?)`,
		conversationID, role, content, tcJSON)
	if err != nil {
		return nil, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE conversations SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, conversationID)
	id, _ := res.LastInsertId()
	return &types.Message{
		ID: id, ConversationID: conversationID, Role: role, Content: content,
		ToolCalls: toolCalls, CreatedAt: time.Now(),
	}, nil
}

// Messages returns the messages of a conversation in order.
func (s *Store) Messages(ctx context.Context, conversationID int64) ([]types.Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, conversation_id, role, content, tool_calls, created_at
		 FROM messages WHERE conversation_id = ? ORDER BY id ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Message
	for rows.Next() {
		var m types.Message
		var tc sql.NullString
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &tc, &m.CreatedAt); err != nil {
			return nil, err
		}
		if tc.Valid && tc.String != "" {
			_ = json.Unmarshal([]byte(tc.String), &m.ToolCalls)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
