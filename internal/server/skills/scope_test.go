package skills

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// openSkillDB opens an in-memory database with the skills/skill_files tables.
func openSkillDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE skills (name TEXT PRIMARY KEY, description TEXT NOT NULL DEFAULT '', updated_by INTEGER, updated_at TIMESTAMP);
		CREATE TABLE skill_files (skill_name TEXT NOT NULL, relpath TEXT NOT NULL, content BLOB NOT NULL, updated_at TIMESTAMP, updated_by INTEGER, PRIMARY KEY(skill_name, relpath));
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func skillMD(desc string) string {
	return "---\nname: foo\ndescription: " + desc + "\n---\nbody\n"
}

// TestScopePrecedence checks that skills resolve project → shared → user.
func TestScopePrecedence(t *testing.T) {
	ctx := context.Background()
	shared := openSkillDB(t)
	user := openSkillDB(t)
	proj := openSkillDB(t)
	m := &Manager{shared: shared}

	// user-only skill "bar" resolves from user.
	if err := writeItem(ctx, user, skillTables, "bar", "u", 0, map[string]string{"SKILL.md": skillMD("user-bar")}); err != nil {
		t.Fatal(err)
	}
	if _, src, err := m.resolveRaw(ctx, user, proj, "bar"); err != nil || src != ScopeUser {
		t.Fatalf("bar: src=%q err=%v (want user)", src, err)
	}

	// "foo" in both user and shared → shared shadows user.
	writeItem(ctx, user, skillTables, "foo", "u", 0, map[string]string{"SKILL.md": skillMD("user-foo")})
	writeItem(ctx, shared, skillTables, "foo", "s", 0, map[string]string{"SKILL.md": skillMD("shared-foo")})
	if _, src, err := m.resolveRaw(ctx, user, nil, "foo"); err != nil || src != ScopeShared {
		t.Fatalf("foo (no proj): src=%q err=%v (want shared)", src, err)
	}

	// project version shadows shared and user.
	writeItem(ctx, proj, skillTables, "foo", "p", 0, map[string]string{
		"SKILL.md": skillMD("proj-foo"),
		"lib/x.js": "// project x",
	})
	files, src, err := m.resolveRaw(ctx, user, proj, "foo")
	if err != nil || src != ScopeProject {
		t.Fatalf("foo (proj): src=%q err=%v (want project)", src, err)
	}
	if files["lib/x.js"] != "// project x" {
		t.Fatalf("nested file not resolved from project scope: %q", files["lib/x.js"])
	}

	// GetFile resolves a nested path from the winning (project) scope.
	content, gsrc, err := m.GetFile(ctx, user, proj, "foo", "lib/x.js")
	if err != nil || gsrc != ScopeProject || content != "// project x" {
		t.Fatalf("GetFile: content=%q src=%q err=%v", content, gsrc, err)
	}
}

// TestScopeDBForDefaults checks the default write scope.
func TestScopeDBForDefaults(t *testing.T) {
	shared := openSkillDB(t)
	user := openSkillDB(t)
	proj := openSkillDB(t)
	m := &Manager{shared: shared}

	if _, sc, _ := m.ScopeDBFor("", user, proj); sc != ScopeProject {
		t.Fatalf("default in project: %q (want project)", sc)
	}
	if _, sc, _ := m.ScopeDBFor("", user, nil); sc != ScopeUser {
		t.Fatalf("default no project: %q (want user)", sc)
	}
	if _, sc, _ := m.ScopeDBFor("shared", user, nil); sc != ScopeShared {
		t.Fatalf("explicit shared: %q", sc)
	}
	if _, _, err := m.ScopeDBFor("project", user, nil); err == nil {
		t.Fatal("project scope without a project should error")
	}
}

// TestSkillFileAudit: a save stamps only the files whose content changed.
func TestSkillFileAudit(t *testing.T) {
	ctx := context.Background()
	db := openSkillDB(t)
	stamp := func(rel string) (by int64, at string) {
		db.QueryRow(`SELECT updated_by, CAST(updated_at AS TEXT) FROM skill_files WHERE skill_name='foo' AND relpath=?`, rel).Scan(&by, &at)
		return
	}
	save := func(uid int64, files map[string]string) {
		t.Helper()
		if err := writeItem(ctx, db, skillTables, "foo", "d", uid, files); err != nil {
			t.Fatal(err)
		}
	}
	save(1, map[string]string{"SKILL.md": skillMD("a"), "lib/x.js": "x"})
	_, at1 := stamp("lib/x.js")
	save(2, map[string]string{"SKILL.md": skillMD("b"), "lib/x.js": "x", "n.md": "new"})
	if by, at := stamp("lib/x.js"); by != 1 || at != at1 {
		t.Errorf("unchanged file restamped: by=%d at=%s (was %s)", by, at, at1)
	}
	if by, _ := stamp("SKILL.md"); by != 2 {
		t.Errorf("changed SKILL.md updated_by=%d, want 2", by)
	}
	if by, _ := stamp("n.md"); by != 2 {
		t.Errorf("new file updated_by=%d, want 2", by)
	}
	save(3, map[string]string{"SKILL.md": skillMD("b")}) // x.js and n.md removed
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM skill_files WHERE skill_name='foo'`).Scan(&n)
	if n != 1 {
		t.Errorf("files after delete = %d, want 1", n)
	}
}
