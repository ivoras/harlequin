package session

import (
	"context"
	"errors"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
)

func TestRewindToUserMessage(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(":memory:", db.User, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := NewStore()
	sess, err := s.Create(ctx, d, 1, "t", "", "REST", "Web")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range [][2]string{
		{"user", "first question"}, {"assistant", ""}, {"tool", "result"}, {"assistant", "first answer"},
		{"user", "second question\n\n@\"u.3/a.pdf\""}, {"assistant", "second answer"}, {"error", "boom"},
		{"user", "third question"},
	} {
		if _, err := s.AddMessage(ctx, d, sess.ID, m[0], m[1], nil); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		msgs, _ := s.Messages(ctx, d, sess.ID)
		return len(msgs)
	}
	// Wrong text or index: refused, nothing deleted.
	if _, err := s.RewindToUserMessage(ctx, d, sess.ID, 2, "something else"); !errors.Is(err, ErrRewindMismatch) || count() != 8 {
		t.Fatalf("mismatch: err=%v count=%d", err, count())
	}
	if _, err := s.RewindToUserMessage(ctx, d, sess.ID, 9, ""); !errors.Is(err, ErrRewindMismatch) {
		t.Fatalf("out of range: %v", err)
	}
	// The client sends the text it shows (no attachment suffix): a prefix match.
	removed, err := s.RewindToUserMessage(ctx, d, sess.ID, 2, "second question")
	if err != nil {
		t.Fatal(err)
	}
	if removed != "second question\n\n@\"u.3/a.pdf\"" || count() != 4 {
		t.Fatalf("removed %q, %d messages left", removed, count())
	}
	msgs, _ := s.Messages(ctx, d, sess.ID)
	if msgs[3].Content != "first answer" {
		t.Fatalf("last kept message %q", msgs[3].Content)
	}
}
