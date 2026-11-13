package agent

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/session"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Personal hats: created and worn by their owner, invisible to others, shadowed
// by a shared hat of the same name, refused on project sessions.
func TestPersonalHats(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	sk := skills.NewManager(mgr.Shared, fstest.MapFS{}, jsrun.New(jsrun.Options{}), nil)
	a := &Agent{Storage: mgr, Sessions: session.NewStore(), Skills: sk, DocEvents: docevents.New(), Runner: jsrun.New(jsrun.Options{})}
	if err := sk.CreateHat(ctx, nil, types.HatScopeShared, "analyst", "shared one", 1); err != nil {
		t.Fatal(err)
	}

	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		// Name clash with a shared hat is refused, in either scope.
		if err := sk.CreateHat(ctx, udb, types.HatScopePersonal, "analyst", "mine", 1); err == nil {
			t.Error("personal hat with a shared name was allowed")
		}
		if err := sk.CreateHat(ctx, udb, types.HatScopePersonal, "mine", "my hat", 1); err != nil {
			t.Fatal(err)
		}
		if err := sk.CreateHat(ctx, udb, types.HatScopePersonal, "mine", "again", 1); err == nil {
			t.Error("duplicate personal hat was allowed")
		}
		h, err := sk.GetHat(ctx, udb, "mine")
		if err != nil || h.Scope != types.HatScopePersonal {
			t.Fatalf("get: %v %+v", err, h)
		}
		if home, scope, _ := sk.HatHome(ctx, udb, "analyst"); home != mgr.Shared || scope != types.HatScopeShared {
			t.Errorf("shared hat resolved to %q", scope)
		}
		list, _ := sk.ListHats(ctx, udb)
		if len(list) != 2 || list[0].Name != "analyst" || list[1].Scope != types.HatScopePersonal {
			t.Errorf("list: %+v", list)
		}

		// Wear it on a personal session; edit it through its home db.
		sess, _ := a.Sessions.Create(ctx, udb, 1, "t", "", types.APIREST, types.InterfaceWeb)
		if err := a.SetSessionHat(ctx, udb, udb, 1, 0, sess.ID, "mine"); err != nil {
			t.Fatal(err)
		}
		if err := sk.PutHatFile(ctx, udb, "mine", "system_prompt.md", "---\ndescription: edited\n---\nROLE", 1); err != nil {
			t.Fatal(err)
		}
		rc := &runContext{sessionID: sess.ID, userID: 1, userDB: udb, sessDB: udb}
		a.loadHat(ctx, rc)
		if rc.hat == nil || rc.hat.Name != "mine" || rc.hat.SystemPrompt != "ROLE" {
			t.Fatalf("loadHat: %+v", rc.hat)
		}

		// Refused on a project session; the shared hat is fine.
		if err := a.SetSessionHat(ctx, udb, udb, 1, 7, sess.ID, "mine"); !errors.Is(err, skills.ErrPersonalHatInProject) {
			t.Errorf("project session: %v", err)
		}
		if err := a.SetSessionHat(ctx, udb, udb, 1, 7, sess.ID, "analyst"); err != nil {
			t.Errorf("shared hat on a project session: %v", err)
		}
		// A project session ignores a personal hat that got onto it anyway, and
		// does not advertise personal hats.
		rc2 := &runContext{sessionID: sess.ID, userID: 1, userDB: udb, sessDB: udb, projectID: 7}
		_ = a.Sessions.SetHat(ctx, udb, sess.ID, "mine")
		a.loadHat(ctx, rc2)
		if rc2.hat != nil {
			t.Errorf("personal hat worn in a project session: %+v", rc2.hat)
		}
		if hats := a.wearableHats(ctx, udb, 7); len(hats) != 1 || hats[0].Name != "analyst" {
			t.Errorf("wearable in project: %+v", hats)
		}
		return nil
	})

	// Another user neither sees nor can wear it.
	_ = mgr.WithUser(ctx, 2, func(udb *sql.DB) error {
		if _, err := sk.GetHat(ctx, udb, "mine"); !errors.Is(err, skills.ErrHatNotFound) {
			t.Errorf("other user got the hat: %v", err)
		}
		// A shared hat appearing later shadows a same-named personal one.
		_ = sk.CreateHat(ctx, udb, types.HatScopePersonal, "later", "p", 2)
		_ = sk.CreateHat(ctx, nil, types.HatScopeShared, "later", "s", 1)
		if h, _ := sk.GetHat(ctx, udb, "later"); h == nil || h.Scope != types.HatScopeShared {
			t.Errorf("shared did not win: %+v", h)
		}
		return nil
	})
}
