package audit

import (
	"context"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/db"
)

func TestSearchAndStats(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(t.TempDir()+"/u.db", db.User, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := NewStore()
	s.Log(ctx, d, "login", "a@x.com", nil)
	s.Log(ctx, d, "login", "b@x.com", nil)
	s.Log(ctx, d, "skill_save", "100%_sure", map[string]string{"k": "v"})

	es, err := s.Search(ctx, d, 1, Filter{Actions: []string{"login"}}, 10)
	if err != nil || len(es) != 2 {
		t.Fatalf("actions filter: %v %d", err, len(es))
	}
	// LIKE wildcards in the query are literal.
	if es, _ = s.Search(ctx, d, 1, Filter{Q: "0%_s"}, 10); len(es) != 1 {
		t.Fatalf("escaped q: %d", len(es))
	}
	if es, _ = s.Search(ctx, d, 1, Filter{Q: "%"}, 10); len(es) != 1 {
		t.Fatalf("bare %% should match only the literal: %d", len(es))
	}
	if es, _ = s.Search(ctx, d, 1, Filter{From: time.Now().Add(time.Hour)}, 10); len(es) != 0 {
		t.Fatalf("future from: %d", len(es))
	}
	a, days, err := s.Stats(ctx, d, Filter{TZ: 120})
	if err != nil || a["login"] != 2 || a["skill_save"] != 1 {
		t.Fatalf("stats: %v %v", err, a)
	}
	n := 0
	for _, c := range days {
		n += c
	}
	if n != 3 {
		t.Fatalf("days: %v", days)
	}
}
