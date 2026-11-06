package session

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/db"
)

func TestReuseAndCleanupEmpty(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(filepath.Join(t.TempDir(), "user.db"), db.User, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := NewStore()

	if c, _ := s.ReuseEmpty(ctx, d, 1, "", "", ""); c != nil {
		t.Fatal("nothing to reuse yet")
	}
	empty, _ := s.Create(ctx, d, 1, "", "", "", "")
	used, _ := s.Create(ctx, d, 1, "", "", "", "")
	s.AddMessage(ctx, d, used.ID, "user", "hi", nil)
	named, _ := s.Create(ctx, d, 1, "Budget", "", "", "")

	got, err := s.ReuseEmpty(ctx, d, 1, "pirate", "", "")
	if err != nil || got == nil || got.ID != empty.ID {
		t.Fatalf("want reuse of %d, got %+v err %v", empty.ID, got, err)
	}
	if got.Hat == nil || *got.Hat != "pirate" {
		t.Errorf("hat not applied: %+v", got.Hat)
	}
	if c, _ := s.ReuseEmpty(ctx, d, 1, "", "WS", "Web"); c != nil {
		t.Error("other transport's session must not be reused")
	}

	// Cleanup spares fresh, used and named sessions; removes only the stale empty one.
	d.Exec(`UPDATE sessions SET updated_at = ?`, time.Now().Add(-8*24*time.Hour).UTC().Format("2006-01-02 15:04:05"))
	fresh, _ := s.Create(ctx, d, 1, "", "", "", "")
	n, err := s.DeleteEmptyPlaceholders(ctx, d, 7*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("deleted %d, err %v; want 1", n, err)
	}
	all, _ := s.List(ctx, d, 1, "")
	ids := map[int64]bool{}
	for _, c := range all {
		ids[c.ID] = true
	}
	if ids[empty.ID] || !ids[used.ID] || !ids[named.ID] || !ids[fresh.ID] {
		t.Errorf("wrong survivors: %v", ids)
	}
}
