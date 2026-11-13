package agent

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/session"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/server/userconfig"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestSetSessionHat(t *testing.T) {
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
	bus := docevents.New()
	sub := bus.Subscribe(1, nil)
	a := &Agent{Storage: mgr, Sessions: session.NewStore(), Skills: sk, DocEvents: bus}

	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		sess, _ := a.Sessions.Create(ctx, udb, 1, "t", "", types.APIREST, types.InterfaceWeb)
		cfg := userconfig.NewStore()
		last := func() string { v, _, _ := cfg.Get(ctx, udb, userconfig.KeyHatLast); return v }

		if err := a.SetSessionHat(ctx, udb, udb, 1, 0, sess.ID, "analyst"); err != nil {
			t.Fatal(err)
		}
		got, _ := a.Sessions.Get(ctx, udb, sess.ID, 1)
		if got.Hat == nil || *got.Hat != "analyst" || last() != "analyst" {
			t.Fatalf("hat %v last %q", got.Hat, last())
		}
		if ev := <-sub.C; ev.Op != docevents.HatChanged || ev.SessionID != sess.ID || ev.Hat != "analyst" {
			t.Fatalf("event %+v", ev)
		}
		if err := a.SetSessionHat(ctx, udb, udb, 1, 0, sess.ID, "nope"); !errors.Is(err, skills.ErrHatNotFound) {
			t.Fatalf("unknown hat: %v", err)
		}
		if last() != "analyst" {
			t.Fatal("failed change altered hat.last")
		}
		if err := a.SetSessionHat(ctx, udb, udb, 1, 0, sess.ID, ""); err != nil {
			t.Fatal(err)
		}
		got, _ = a.Sessions.Get(ctx, udb, sess.ID, 1)
		if (got.Hat != nil && *got.Hat != "") || last() != "" {
			t.Fatalf("not cleared: %v %q", got.Hat, last())
		}
		return nil
	})
}

func TestWearHatTool(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	sk := skills.NewManager(mgr.Shared, fstest.MapFS{}, jsrun.New(jsrun.Options{}), nil)
	_ = sk.CreateHat(ctx, nil, types.HatScopeShared, "analyst", "compares documents", 1)
	_ = sk.CreateHat(ctx, nil, types.HatScopeShared, "researcher", "looks things up", 1)
	a := &Agent{Storage: mgr, Sessions: session.NewStore(), Skills: sk, DocEvents: docevents.New(), Runner: jsrun.New(jsrun.Options{})}
	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		sess, _ := a.Sessions.Create(ctx, udb, 1, "t", "", types.APIREST, types.InterfaceWeb)
		rc := &runContext{sessionID: sess.ID, userID: 1, userDB: udb, sessDB: udb}
		wear := a.buildTools(ctx, rc)["wear_hat"].handler
		worn := func() string {
			s, _ := a.Sessions.Get(ctx, udb, sess.ID, 1)
			if s.Hat == nil {
				return ""
			}
			return *s.Hat
		}
		if out, _ := wear(ctx, rc, map[string]any{"name": "analyst"}); worn() != "analyst" || strings.HasPrefix(out, "error") {
			t.Fatalf("wear: %q worn=%q", out, worn())
		}
		out, _ := wear(ctx, rc, map[string]any{"name": "nope"})
		if !strings.HasPrefix(out, "error") || !strings.Contains(out, "analyst, researcher") || worn() != "analyst" {
			t.Fatalf("unknown hat: %q worn=%q", out, worn())
		}
		if _, _ = wear(ctx, rc, map[string]any{"name": ""}); worn() != "" {
			t.Fatalf("not taken off: %q", worn())
		}
		// The catalogue lists the hats and says not to switch unasked.
		p := a.hatPrompt(ctx, &runContext{})
		if !strings.Contains(p, "- analyst: compares documents") || !strings.Contains(p, "Never switch hats unless") {
			t.Fatalf("catalogue missing: %q", p)
		}
		return nil
	})
}
