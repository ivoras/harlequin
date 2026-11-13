package cron

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestHatOnlyOnSkillJobs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	s := NewStore()
	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		if _, err := s.Create(ctx, udb, types.CreateCronJobRequest{Name: "k", Spec: "@daily", Kind: types.CronKindJS, Target: "1", Hat: "analyst"}); err == nil {
			t.Error("js job accepted a hat")
		}
		j, err := s.Create(ctx, udb, types.CreateCronJobRequest{Name: "s", Spec: "@daily", Kind: types.CronKindSkill, Target: "x", Prompt: "p", Hat: "analyst"})
		if err != nil || j.Hat != "analyst" {
			t.Fatalf("create: %v %+v", err, j)
		}
		js := types.CronKindJS
		if _, err := s.Update(ctx, udb, j.ID, types.UpdateCronJobRequest{Kind: &js}); err == nil {
			t.Error("switching a hatted job to js was allowed")
		}
		none := ""
		if j, err = s.Update(ctx, udb, j.ID, types.UpdateCronJobRequest{Hat: &none, Kind: &js}); err != nil || j.Hat != "" || j.Kind != types.CronKindJS {
			t.Errorf("clear hat then js: %v %+v", err, j)
		}
		return nil
	})
}
