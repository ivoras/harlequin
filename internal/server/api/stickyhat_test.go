package api

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/server/userconfig"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestStickyHat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	sk := skills.NewManager(mgr.Shared, fstest.MapFS{}, jsrun.New(jsrun.Options{}), nil)
	if err := sk.CreateHat(ctx, nil, types.HatScopeShared, "analyst", "d", 1); err != nil {
		t.Fatal(err)
	}
	s := &Server{Skills: sk, UserConfig: userconfig.NewStore()}
	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		if got := s.stickyHat(ctx, udb, ""); got != "" {
			t.Fatalf("nothing worn yet: %q", got)
		}
		s.UserConfig.Set(ctx, udb, userconfig.KeyHatLast, "analyst")
		if got := s.stickyHat(ctx, udb, ""); got != "analyst" {
			t.Fatalf("inherit: %q", got)
		}
		if got := s.stickyHat(ctx, udb, "other"); got != "other" {
			t.Fatalf("explicit wins: %q", got)
		}
		s.UserConfig.Set(ctx, udb, userconfig.KeyHatLast, "deleted")
		if got := s.stickyHat(ctx, udb, ""); got != "" {
			t.Fatalf("deleted hat inherited: %q", got)
		}
		return nil
	})
}

func TestDropGoneHats(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	sk := skills.NewManager(mgr.Shared, fstest.MapFS{}, jsrun.New(jsrun.Options{}), nil)
	_ = sk.CreateHat(ctx, nil, types.HatScopeShared, "analyst", "d", 1)
	s := &Server{Skills: sk}
	a, b := "analyst", "gone"
	list := []types.Session{{Hat: &a}, {Hat: &b}, {}}
	s.dropGoneHats(ctx, nil, list)
	if list[0].Hat == nil || list[1].Hat != nil {
		t.Fatalf("%+v", list)
	}
}
