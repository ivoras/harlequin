// Package userconfig is a generic per-user key/value config store backed by the
// `config` table in each user's database. It holds small settings that don't
// warrant a dedicated table — for example registering a Telegram connection. Like
// the conversation/notify stores it is stateless: every method takes the user's
// DB handle.
package userconfig

import (
	"context"
	"database/sql"
)

// Well-known config keys (extend as more interfaces are added).
const (
	KeyTelegramChatID    = "telegram.chat_id"  // the chat to deliver/receive on
	KeyTelegramUsername  = "telegram.username" // the registered Telegram handle
	KeyTelegramInterface = "telegram.interface"
)

// Store is stateless CRUD over a user's config table.
type Store struct{}

// NewStore constructs a config Store.
func NewStore() *Store { return &Store{} }

// Get returns the value for key (ok=false when absent).
func (s *Store) Get(ctx context.Context, db *sql.DB, key string) (string, bool, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// Set upserts key=value.
func (s *Store) Set(ctx context.Context, db *sql.DB, key, value string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO config(key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value)
	return err
}

// Delete removes key (no error if absent).
func (s *Store) Delete(ctx context.Context, db *sql.DB, key string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM config WHERE key = ?`, key)
	return err
}

// All returns every key/value for the user.
func (s *Store) All(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM config ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
