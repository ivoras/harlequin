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

// sysProvider answers "ok" and remembers the system prompt it was sent.
type sysProvider struct{ sys *string }

func (sysProvider) Name() string { return "sys" }
func (p sysProvider) Chat(_ context.Context, req llm.ChatRequest) (<-chan llm.Chunk, error) {
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			*p.sys = m.Content
		}
	}
	ch := make(chan llm.Chunk, 2)
	ch <- llm.Chunk{TextDelta: "ok"}
	ch <- llm.Chunk{Done: true, Model: "fake"}
	close(ch)
	return ch, nil
}

func TestCronJobRunsWearingItsHat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	sk := skills.NewManager(mgr.Shared, fstest.MapFS{}, jsrun.New(jsrun.Options{}), nil)
	if err := sk.CreateHat(ctx, "analyst", "d", 1); err != nil {
		t.Fatal(err)
	}
	var sys string
	a := &Agent{Provider: sysProvider{&sys}, Storage: mgr, Sessions: session.NewStore(), MaxSteps: 4, Skills: sk,
		Runner: jsrun.New(jsrun.Options{})}
	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		job := types.CronJob{Name: "j", Spec: "@daily", Kind: types.CronKindSkill, Target: "x", Prompt: "hi", Hat: "analyst"}
		if _, err := a.RunCronSkill(ctx, 1, "u@x", types.RoleUser, udb, job); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(sys, `wearing the "analyst" hat`) {
			t.Fatalf("hat not worn; system prompt: %.200s", sys)
		}
		// Deleted hat: the run fails loudly instead of running hatless.
		if err := sk.DeleteHat(ctx, "analyst"); err != nil {
			t.Fatal(err)
		}
		if _, err := a.RunCronSkill(ctx, 1, "u@x", types.RoleUser, udb, job); err == nil || !strings.Contains(err.Error(), "no longer exists") {
			t.Fatalf("want hat-gone error, got %v", err)
		}
		return nil
	})
}
