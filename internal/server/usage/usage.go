// Package usage records per-completion token usage and estimated cost. Usage
// lives in the owning user's per-user database, so methods take that handle.
package usage

import (
	"context"
	"database/sql"
	"time"

	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Store records and queries usage. It holds only the price table; the database
// handle is supplied per call.
type Store struct {
	prices map[string]config.Price
}

// NewStore constructs a usage Store with a price table.
func NewStore(prices map[string]config.Price) *Store {
	return &Store{prices: prices}
}

// EstimateCost returns the estimated USD cost for a completion.
func (s *Store) EstimateCost(model string, promptTokens, completionTokens int) float64 {
	p, ok := s.prices[model]
	if !ok {
		return 0
	}
	return float64(promptTokens)/1000*p.PromptPer1K + float64(completionTokens)/1000*p.CompletionPer1K
}

// Record stores a usage row in the user's database.
func (s *Store) Record(ctx context.Context, db *sql.DB, conversationID *int64, provider, model string, promptTokens, completionTokens int) error {
	cost := s.EstimateCost(model, promptTokens, completionTokens)
	var cid any
	if conversationID != nil {
		cid = *conversationID
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO usage(conversation_id, provider, model, prompt_tokens, completion_tokens, est_cost_usd)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		cid, provider, model, promptTokens, completionTokens, cost)
	return err
}

// Query returns usage rows for the user (the owner of db) within a time window
// (zero times = unbounded).
func (s *Store) Query(ctx context.Context, db *sql.DB, userID int64, from, to time.Time) ([]types.UsageRecord, error) {
	q := `SELECT id, conversation_id, provider, model, prompt_tokens, completion_tokens, est_cost_usd, created_at
		FROM usage`
	var args []any
	var conds []string
	if !from.IsZero() {
		conds = append(conds, `created_at >= ?`)
		args = append(args, from)
	}
	if !to.IsZero() {
		conds = append(conds, `created_at <= ?`)
		args = append(args, to)
	}
	for i, c := range conds {
		if i == 0 {
			q += ` WHERE ` + c
		} else {
			q += ` AND ` + c
		}
	}
	q += ` ORDER BY created_at DESC LIMIT 1000`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.UsageRecord
	for rows.Next() {
		r := types.UsageRecord{UserID: userID}
		var cid sql.NullInt64
		if err := rows.Scan(&r.ID, &cid, &r.Provider, &r.Model, &r.PromptTokens, &r.CompletionTokens, &r.EstCostUSD, &r.CreatedAt); err != nil {
			return nil, err
		}
		if cid.Valid {
			r.ConversationID = &cid.Int64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
