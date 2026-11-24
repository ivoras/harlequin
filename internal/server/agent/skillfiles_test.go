package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/skills/jstmpl"
	"github.com/ivoras/harlequin/internal/server/storage"
)

// load_skill shows SKILL.md first and the reference files, but only lists
// scripts; read_doc reads any skill file by its skill:// path.
func TestSkillFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	sm := skills.NewManager(mgr.Shared, fstest.MapFS{}, jsrun.New(jsrun.Options{}), func(int64, string, string) jstmpl.Context { return jstmpl.Context{} })
	big := strings.Repeat("function helper() { return 1; }\n", 1000)
	if err := sm.Save(ctx, mgr.Shared, "demo", 1, map[string]string{
		"SKILL.md":           "---\nname: demo\ndescription: d\n---\nRead reference/codes.md.",
		"reference/codes.md": "| code | meaning |",
		"lib/helper.js":      big,
		"a-notes.txt":        "notes",
	}); err != nil {
		t.Fatal(err)
	}
	a := &Agent{DataDir: dir, Storage: mgr, Skills: sm, Docs: documents.NewStore(mgr.Shared, nil)}
	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		rc := &runContext{userID: 1, userDB: udb}
		tools := a.buildTools(ctx, rc)
		out, err := tools["load_skill"].handler(ctx, rc, map[string]any{"name": "demo"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out, "=== SKILL.md ===") || !strings.Contains(out, "=== a-notes.txt ===") || !strings.Contains(out, "| code | meaning |") {
			t.Fatalf("load_skill:\n%s", out)
		}
		if strings.Contains(out, "function helper") || !strings.Contains(out, "skill://demo/lib/helper.js (32 KB)") {
			t.Fatalf("script not just listed:\n%.500s", out)
		}
		if strings.Index(out, "=== a-notes.txt ===") > strings.Index(out, "=== reference/codes.md ===") {
			t.Fatal("files not in path order")
		}
		out, _ = tools["read_doc"].handler(ctx, rc, map[string]any{"doc": "skill://demo/reference/codes.md"})
		if !strings.Contains(out, "| code | meaning |") || !strings.Contains(out, "skill file skill://demo/reference/codes.md") {
			t.Fatalf("read_doc skill file: %s", out)
		}
		out, _ = tools["read_doc"].handler(ctx, rc, map[string]any{"doc": "skill://demo/lib/helper.js", "limit": float64(40)})
		if !strings.Contains(out, "function helper") || !strings.Contains(out, "[truncated") {
			t.Fatalf("read_doc script window: %s", out)
		}
		out, _ = tools["read_doc"].handler(ctx, rc, map[string]any{"doc": "skill://demo/nope.md"})
		if !strings.Contains(out, "its files are: SKILL.md, a-notes.txt, lib/helper.js, reference/codes.md") {
			t.Fatalf("missing file: %s", out)
		}
		out, _ = tools["read_doc"].handler(ctx, rc, map[string]any{"doc": "skill://ghost/x.md"})
		if !strings.Contains(out, `no skill "ghost"`) {
			t.Fatalf("missing skill: %s", out)
		}
		return nil
	})
}

// load_skill shows .md files rendered, so a rewrite that drops the <?js ?>
// code the model never saw is refused; one that keeps it goes through.
func TestWriteSkillKeepsTemplateCode(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	sm := skills.NewManager(mgr.Shared, fstest.MapFS{}, jsrun.New(jsrun.Options{}), func(int64, string, string) jstmpl.Context { return jstmpl.Context{} })
	a := &Agent{DataDir: dir, Storage: mgr, Skills: sm, Docs: documents.NewStore(mgr.Shared, nil)}
	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		rc := &runContext{userID: 1, userDB: udb}
		tools := a.buildTools(ctx, rc)
		write := func(content string) string {
			out, _ := tools["write_skill"].handler(ctx, rc, map[string]any{"name": "tpl", "content": content})
			return out
		}
		if out := write("---\nname: tpl\ndescription: d\n---\nHi <?js print(1+1) ?>"); strings.HasPrefix(out, "error") {
			t.Fatalf("create: %s", out)
		}
		if out := write("---\nname: tpl\ndescription: d\n---\nHi 2, and more"); !strings.Contains(out, "template code") {
			t.Fatalf("rendered rewrite not refused: %s", out)
		}
		if out := write("---\nname: tpl\ndescription: d\n---\nHello <?js print(1+1) ?>"); strings.HasPrefix(out, "error") {
			t.Fatalf("rewrite keeping the template refused: %s", out)
		}
		return nil
	})
}
