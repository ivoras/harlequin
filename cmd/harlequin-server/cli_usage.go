package main

import (
	"context"

	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/server/usage"
)

// cliRecorder is the server's usage recorder for a one-off command: a call with
// an acting user lands in that user's usage table, any other in
// usage_unattributed — the same rows, prices and totals as the running server,
// so a reindex or re-derivation that spends on the provider's key shows up in
// the usage views instead of only on the provider's bill.
func cliRecorder(cfg *config.Config, store *storage.Manager) llm.UsageRecorder {
	return recordUsage(store, usage.NewStore(cfg.Prices))
}

// embedderUsage adapts a recorder to the embedder's usage hook: embeddings are
// billed like completions (prompt tokens only).
func embedderUsage(rec llm.UsageRecorder) embed.UsageFunc {
	return func(ctx context.Context, model string, tokens int, cost *float64, _ bool) {
		rec(ctx, "embed", model, llm.Usage{PromptTokens: tokens, TotalTokens: tokens, Cost: cost})
	}
}
