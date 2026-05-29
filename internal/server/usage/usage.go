// Package usage records per-completion token usage and estimated cost.
package usage

import (
	"context"
	"database/sql"
	"time"

	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Store records and queries usage.
type Store struct {
	db     *sql.DB
	prices map[string]config.Price
}

// NewStore constructs a usage Store with a price table.
func NewStore(db *sql.DB, prices map[string]config.Price) *Store {
	return &Store{db: db, prices: prices}
}

// EstimateCost returns the estimated USD cost for a completion.
func (s *Store) EstimateCost(model string, promptTokens, completionTokens int) float64 {
	p, ok := s.prices[model]
	if !ok {
		return 0
	}
	return float64(promptTokens)/1000*p.PromptPer1K + float64(completionTokens)/1000*p.CompletionPer1K
}

// Record stores a usage row.
func (s *Store) Record(ctx context.Context, userID int64, conversationID *int64, provider, model string, promptTokens, completionTokens int) error {
	cost := s.EstimateCost(model, promptTokens, completionTokens)
	var cid any
	if conversationID != nil {
		cid = *conversationID
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage(user_id, conversation_id, provider, model, prompt_tokens, completion_tokens, est_cost_usd)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, cid, provider, model, promptTokens, completionTokens, cost)
	return err
}

// Query returns usage rows for a user within a time window (zero times = unbounded).
func (s *Store) Query(ctx context.Context, userID int64, from, to time.Time) ([]types.UsageRecord, error) {
	q := `SELECT id, user_id, conversation_id, provider, model, prompt_tokens, completion_tokens, est_cost_usd, created_at
		FROM usage WHERE user_id = ?`
	args := []any{userID}
	if !from.IsZero() {
		q += ` AND created_at >= ?`
		args = append(args, from)
	}
	if !to.IsZero() {
		q += ` AND created_at <= ?`
		args = append(args, to)
	}
	q += ` ORDER BY created_at DESC LIMIT 1000`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.UsageRecord
	for rows.Next() {
		var r types.UsageRecord
		var cid sql.NullInt64
		if err := rows.Scan(&r.ID, &r.UserID, &cid, &r.Provider, &r.Model, &r.PromptTokens, &r.CompletionTokens, &r.EstCostUSD, &r.CreatedAt); err != nil {
			return nil, err
		}
		if cid.Valid {
			r.ConversationID = &cid.Int64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
