package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/session"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// A model that keeps calling tools must still answer on the last step (no
// tools, nudged), and a turn that ends without any answer must fail loudly
// with a stored error instead of silently ending on a tool result.
func TestMaxStepsForcesAnswerOrFailsLoudly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider llm.Provider
		wantErr  bool
	}{
		{"forced answer", toolLooperProvider{finalText: "Here is what I found."}, false},
		{"no answer", toolLooperProvider{finalText: ""}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
			if err != nil {
				t.Fatal(err)
			}
			defer mgr.Close()
			sessions := session.NewStore()
			a := &Agent{
				Provider: tc.provider, Storage: mgr, Sessions: sessions, MaxSteps: 4,
				Skills: skills.NewManager(mgr.Shared, fstest.MapFS{}, jsrun.New(jsrun.Options{}), nil),
			}
			var sessID int64
			_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
				s, err := sessions.Create(ctx, udb, 1, "t", "", types.APIREST, types.InterfaceWeb)
				sessID = s.ID
				return err
			})
			var events []types.StreamEvent
			err = a.Run(ctx, 0, sessID, 1, "u@x", types.RoleUser, "", "", "question?", func(ev types.StreamEvent) { events = append(events, ev) })
			sawErr := false
			for _, ev := range events {
				sawErr = sawErr || (ev.Type == types.SSEError && strings.Contains(ev.Error, "used all 4 steps"))
			}
			var last string
			_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
				msgs, _ := sessions.Messages(ctx, udb, sessID)
				if len(msgs) > 0 {
					last = msgs[len(msgs)-1].Role + ": " + msgs[len(msgs)-1].Content
				}
				return nil
			})
			if tc.wantErr {
				if err == nil || !sawErr || !strings.HasPrefix(last, llm.RoleError+":") {
					t.Fatalf("want loud failure; err=%v sawErr=%v last=%q", err, sawErr, last)
				}
				return
			}
			if err != nil || last != "assistant: Here is what I found." {
				t.Fatalf("want forced answer; err=%v last=%q", err, last)
			}
		})
	}
}
