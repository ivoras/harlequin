package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/storage"
)

// A one-off command's embedding calls are recorded: unattributed ones in the
// system db, attributed ones in the user's usage table, both priced from config.
func TestCLIUsageRecorded(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := &config.Config{}
	hook := embedderUsage(cliRecorder(cfg, store))

	hook(ctx, "m/embed", 1500, nil, false) // no acting user
	var n, tokens int
	if err := store.System.QueryRow(`SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0) FROM usage_unattributed`).Scan(&n, &tokens); err != nil || n != 1 || tokens != 1500 {
		t.Fatalf("unattributed: rows=%d tokens=%d err=%v", n, tokens, err)
	}

	hook(llm.WithAttribution(ctx, llm.Attribution{UserID: 1}), "m/embed", 700, nil, false)
	if err := store.WithUser(ctx, 1, func(db *sql.DB) error {
		return db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0) FROM usage WHERE provider = 'embed'`).Scan(&n, &tokens)
	}); err != nil || n != 1 || tokens != 700 {
		t.Fatalf("attributed: rows=%d tokens=%d err=%v", n, tokens, err)
	}
}
