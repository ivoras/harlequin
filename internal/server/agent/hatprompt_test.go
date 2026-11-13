package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/skills/jstmpl"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestHatPromptAdditiveByDefault(t *testing.T) {
	fsys := fstest.MapFS{"skills/system_prompt.md": {Data: []byte("DEFAULT RULES")}}
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	a := &Agent{Skills: skills.NewManager(mgr.Shared, fsys, jsrun.New(jsrun.Options{}),
		func(int64, string, string) jstmpl.Context { return jstmpl.Context{} })}
	rc := func(h types.Hat) *runContext { return &runContext{hat: &h} }

	got := a.hatPrompt(context.Background(), rc(types.Hat{Name: "x", SystemPrompt: "ROLE"}))
	if !strings.Contains(got, "DEFAULT RULES") || !strings.Contains(got, "ROLE") || !strings.Contains(got, `"x" hat`) {
		t.Fatalf("additive: %q", got)
	}
	got = a.hatPrompt(context.Background(), rc(types.Hat{Name: "x", SystemPrompt: "ROLE", ReplacePrompt: true}))
	if strings.Contains(got, "DEFAULT RULES") || !strings.Contains(got, "ROLE") {
		t.Fatalf("replace: %q", got)
	}
	// use_prompt: false leaves SystemPrompt empty (hatFromPrompt), so only the default remains.
	got = a.hatPrompt(context.Background(), rc(types.Hat{Name: "x", ReplacePrompt: true}))
	if !strings.Contains(got, "DEFAULT RULES") {
		t.Fatalf("disabled body: %q", got)
	}
}
