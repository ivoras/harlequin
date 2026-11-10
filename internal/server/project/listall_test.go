package project

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
)

// ListAll shows projects the viewer isn't in; List does not.
func TestListAllIncludesNonMemberProjects(t *testing.T) {
	sys, err := db.Open(filepath.Join(t.TempDir(), "harlequin.db"), db.System, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer sys.Close()
	for _, e := range []string{"a@x", "b@x"} {
		sys.Exec(`INSERT INTO users(email, password_hash, role) VALUES (?, 'x', 'user')`, e)
	}
	ctx := context.Background()
	s := NewStore(sys)
	s.Create(ctx, "mine", 1)
	s.Create(ctx, "theirs", 2)
	own, _ := s.List(ctx, 1)
	all, err := s.ListAll(ctx)
	if err != nil || len(own) != 1 || len(all) != 2 || len(all[0].Members) != 1 {
		t.Fatalf("own=%d all=%d err=%v", len(own), len(all), err)
	}
}
