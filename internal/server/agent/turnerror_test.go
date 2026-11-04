package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/session"
)

// A failed turn's error is stored as a role "error" message (so every client
// shows it after a reload), but not for an interrupted turn.
func TestRecordTurnError(t *testing.T) {
	sqlDB, err := db.Open(":memory:", db.User, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	store := session.NewStore()
	ctx := context.Background()
	sess, err := store.Create(ctx, sqlDB, 1, "t", "", "REST", "TUI")
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Sessions: store}
	rc := &runContext{sessionID: sess.ID, sessDB: sqlDB}

	a.recordTurnError(ctx, rc, errors.New("provider down"))
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	a.recordTurnError(cctx, rc, errors.New("interrupted")) // user stop: not recorded
	a.recordTurnError(ctx, rc, nil)

	msgs, err := store.Messages(ctx, sqlDB, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Role != llm.RoleError || msgs[0].Content != "provider down" {
		t.Fatalf("messages = %+v, want one stored error", msgs)
	}
}
