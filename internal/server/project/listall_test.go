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

// With adminsSeeAll, owners/admins are treated as members of every existing
// project; ordinary users and the switch-off state are unaffected.
func TestAdminsSeeAll(t *testing.T) {
	sys, err := db.Open(filepath.Join(t.TempDir(), "harlequin.db"), db.System, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer sys.Close()
	for i, r := range []string{"user", "user", "admin", "owner"} {
		sys.Exec(`INSERT INTO users(email, password_hash, role) VALUES (?, 'x', ?)`, string(rune('a'+i))+"@x", r)
	}
	ctx := context.Background()
	s := NewStore(sys)
	s.Create(ctx, "mine", 1)
	s.Create(ctx, "theirs", 2)
	check := func(on bool, user int64, wantList int, wantMember bool) {
		t.Helper()
		s.SetAdminsSeeAll(on)
		l, err := s.List(ctx, user)
		m, _ := s.IsMember(ctx, 2, user)
		if err != nil || len(l) != wantList || m != wantMember {
			t.Errorf("on=%v user=%d: list=%d member=%v err=%v", on, user, len(l), m, err)
		}
	}
	check(true, 1, 1, false) // ordinary user: own projects only
	check(true, 3, 2, true)  // admin
	check(true, 4, 2, true)  // owner
	check(false, 3, 0, false)
	s.SetAdminsSeeAll(true)
	l, _ := s.List(ctx, 3)
	for _, p := range l {
		if !p.AdminAccess {
			t.Errorf("admin who is in no project: %q should be flagged AdminAccess", p.Name)
		}
	}
	if l, _ := s.List(ctx, 1); len(l) != 1 || l[0].AdminAccess {
		t.Errorf("a real member's own project must not be flagged: %+v", l)
	}
	if m, _ := s.IsMember(ctx, 99, 4); m {
		t.Error("owner is not a member of a project that does not exist")
	}
}
