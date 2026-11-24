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

// The file tools work on skill files like on sandbox files: write_skill only
// creates, read_doc shows the stored source, edit_doc/write_file change it (a
// rewrite that drops <?js ?> code is refused), and delete_doc removes one file.
func TestFileToolsOnSkills(t *testing.T) {
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
		call := func(tool string, args map[string]any) string {
			out, err := tools[tool].handler(ctx, rc, args)
			if err != nil {
				t.Fatalf("%s: %v", tool, err)
			}
			return out
		}
		want := func(out, sub string) {
			t.Helper()
			if !strings.Contains(out, sub) {
				t.Fatalf("want %q in: %s", sub, out)
			}
		}
		md := "---\nname: tpl\ndescription: d\n---\nHi <?js print(1+1) ?>\n"
		want(call("write_skill", map[string]any{"name": "tpl", "content": md}), "Saved skill://tpl/SKILL.md")
		want(call("write_skill", map[string]any{"name": "tpl", "content": md}), "already exists")
		want(call("read_doc", map[string]any{"doc": "skill://tpl/SKILL.md"}), "<?js print(1+1) ?>")
		want(call("load_skill", map[string]any{"name": "tpl"}), "Hi 2")
		want(call("write_file", map[string]any{"path": "skill://tpl/SKILL.md", "content": "---\nname: tpl\ndescription: d\n---\nHi 2"}), "already exists")
		want(call("write_file", map[string]any{"path": "skill://tpl/SKILL.md", "content": "---\nname: tpl\ndescription: d\n---\nHi 2", "overwrite": true}), "template code")
		want(call("edit_doc", map[string]any{"doc": "skill://tpl/SKILL.md", "old_string": "Hi ", "new_string": "Hello "}), "Saved skill://tpl/SKILL.md")
		want(call("read_doc", map[string]any{"doc": "skill://tpl/SKILL.md"}), "Hello <?js")
		want(call("write_file", map[string]any{"path": "skill://tpl/lib/x.js", "content": "println('needle')"}), "Saved skill://tpl/lib/x.js")
		want(call("write_file", map[string]any{"path": "skill://nope/lib/x.js", "content": "x"}), "create it first")
		want(call("list_documents", map[string]any{"path": "skill://tpl/"}), "skill://tpl/lib/x.js")
		want(call("Grep", map[string]any{"pattern": "needle", "path": "skill://tpl/"}), "skill://tpl/lib/x.js")
		want(call("copy_doc", map[string]any{"source": "skill://tpl/lib/x.js", "target": "tmp://x.js"}), "Copied")
		want(call("delete_doc", map[string]any{"doc": "skill://tpl/SKILL.md"}), "delete_skill")
		want(call("delete_doc", map[string]any{"doc": "skill://tpl/lib/x.js"}), "Deleted lib/x.js")
		want(call("read_doc", map[string]any{"doc": "skill://tpl/lib/x.js"}), "has no file")
		// sandbox files: write, refuse a blind overwrite, delete
		want(call("write_file", map[string]any{"path": "tmp://n.md", "content": "a"}), "Wrote tmp://n.md")
		want(call("write_file", map[string]any{"path": "tmp://n.md", "content": "b"}), "already exists")
		want(call("write_file", map[string]any{"path": "u.3", "content": "b"}), "saved document")
		want(call("delete_doc", map[string]any{"doc": "tmp://n.md"}), "Deleted tmp://n.md")
		want(call("delete_doc", map[string]any{"doc": "tmp://n.md"}), "does not exist")
		// retired names still work but are not offered
		want(call("write_skill_file", map[string]any{"name": "tpl", "path": "r.md", "content": "ref"}), "Saved skill://tpl/r.md")
		want(call("edit_file", map[string]any{"path": "tmp://m.md", "append": "x"}), "file is now")
		for _, n := range []string{"edit_file", "write_skill_file"} {
			if !tools[n].hidden {
				t.Errorf("%s should be hidden", n)
			}
		}
		return nil
	})
}
