package project

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
)

func TestDeleteRemovesProjectMembersAndInvites(t *testing.T) {
	sys, err := db.Open(filepath.Join(t.TempDir(), "harlequin.db"), db.System, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer sys.Close()
	ctx := context.Background()
	for _, e := range []string{"a@x", "b@x"} {
		if _, err := sys.Exec(`INSERT INTO users(email, password_hash, role) VALUES (?, 'x', 'user')`, e); err != nil {
			t.Fatal(err)
		}
	}
	s := NewStore(sys)
	keep, err := s.Create(ctx, "keep", 1)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := s.Create(ctx, "gone", 1)
	if err != nil {
		t.Fatal(err)
	}
	invID, err := s.Invite(ctx, gone.ID, 2, 1)
	if err != nil || invID == 0 {
		t.Fatalf("invite id %d: %v", invID, err)
	}
	// A re-invite updates the same row and returns its id (alerts key on it).
	if again, err := s.Invite(ctx, gone.ID, 2, 1); err != nil || again != invID {
		t.Fatalf("re-invite id %d, want %d (%v)", again, invID, err)
	}

	if err := s.Delete(ctx, gone.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, gone.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Get after delete = %v, want ErrNoRows", err)
	}
	if ok, _ := s.IsMember(ctx, gone.ID, 1); ok {
		t.Error("membership survived project deletion")
	}
	if inv, _ := s.ListInvites(ctx, 2); len(inv) != 0 {
		t.Errorf("invites survived project deletion: %+v", inv)
	}
	if ok, _ := s.IsMember(ctx, keep.ID, 1); !ok {
		t.Error("other project's membership was removed")
	}
	if err := s.Delete(ctx, gone.ID); err != nil {
		t.Errorf("second Delete should be a no-op: %v", err)
	}
}
