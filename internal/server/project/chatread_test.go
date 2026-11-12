package project

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/db"
)

func TestAbridge(t *testing.T) {
	if got := Abridge(" a  b\nc ", 15); got != "a b c" {
		t.Fatalf("%q", got)
	}
	if got := Abridge("1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16", 15); got != "1 2 3 4 5 6 7 8 9 10 11 12 13 14 15…" {
		t.Fatalf("%q", got)
	}
}

func TestChatUnread(t *testing.T) {
	dir := t.TempDir()
	sys, err := db.Open(filepath.Join(dir, "harlequin.db"), db.System, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer sys.Close()
	usr, err := db.Open(filepath.Join(dir, "user.db"), db.User, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer usr.Close()
	pdb, err := db.Open(filepath.Join(dir, "project.db"), db.Project, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer pdb.Close()
	for _, e := range []string{"me@x", "you@x"} {
		sys.Exec(`INSERT INTO users(email, password_hash, role) VALUES (?, 'x', 'user')`, e)
	}
	ctx := context.Background()
	s := NewStore(sys)
	add := func(uid int64, text string) {
		if _, err := s.AddChatMessage(ctx, pdb, uid, text); err != nil {
			t.Fatal(err)
		}
	}
	// 1: before I joined, 2: mine, 3 and 4: from "you".
	add(2, "old news")
	add(1, "my own message")
	add(2, "first")
	add(2, "second one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen")

	// No marker: everything since joining counts (join time in the past here
	// includes all; a join after message 1 excludes it), never my own.
	n, msgs, err := s.ChatUnread(ctx, pdb, 1, 0, false, time.Time{})
	if err != nil || n != 3 || len(msgs) != 3 || msgs[0].Snippet != "old news" || msgs[2].Email != "you@x" {
		t.Fatalf("no marker: n=%d msgs=%+v err=%v", n, msgs, err)
	}
	if got := msgs[2].Snippet; got != "second one two three four five six seven eight nine ten eleven twelve thirteen fourteen…" {
		t.Fatalf("snippet %q", got)
	}
	if n, _, _ := s.ChatUnread(ctx, pdb, 1, 0, false, time.Now().Add(time.Hour)); n != 0 {
		t.Fatalf("joined in the future: %d", n)
	}

	// Marking read never lowers the marker and drives the unread set.
	if err := MarkChatRead(ctx, usr, 7, 3); err != nil {
		t.Fatal(err)
	}
	if err := MarkChatRead(ctx, usr, 7, 1); err != nil {
		t.Fatal(err)
	}
	marks, _ := ChatReadMarks(ctx, usr)
	if marks[7] != 3 {
		t.Fatalf("marker %d, want 3", marks[7])
	}
	n, msgs, _ = s.ChatUnread(ctx, pdb, 1, marks[7], true, time.Time{})
	if n != 1 || msgs[0].ID != 4 {
		t.Fatalf("after marker: n=%d msgs=%+v", n, msgs)
	}
	if max, _ := MaxChatID(ctx, pdb); max != 4 {
		t.Fatalf("max %d", max)
	}
}
