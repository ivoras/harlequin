package skills

import (
	"context"
	"testing"
)

func TestValidateRelpaths(t *testing.T) {
	t.Parallel()
	good := []string{"SKILL.md", "lib/x.js", "a/b/c.txt"}
	for _, rel := range good {
		if err := validateRelpaths(map[string]string{rel: ""}); err != nil {
			t.Errorf("%q rejected: %v", rel, err)
		}
	}
	bad := []string{"", "/etc/passwd", "../SKILL.md", "a/../../x", "a/./b", "a//b", `a\b`, ".."}
	for _, rel := range bad {
		if err := validateRelpaths(map[string]string{rel: ""}); err == nil {
			t.Errorf("%q accepted, want error", rel)
		}
	}
}

// TestRepairDescriptions checks that empty description columns (the
// skill_overrides back-fill migration) are healed from SKILL.md frontmatter.
func TestRepairDescriptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openSkillDB(t)
	// Simulate the back-fill: row present, description empty, files intact.
	if _, err := db.Exec(`INSERT INTO skills(name, description) VALUES ('web-x', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO skill_files(skill_name, relpath, content) VALUES ('web-x', 'SKILL.md', ?)`,
		[]byte(skillMD("watch a page"))); err != nil {
		t.Fatal(err)
	}
	// A row whose description is already set must be left alone.
	if _, err := db.Exec(`INSERT INTO skills(name, description) VALUES ('ok', 'kept')`); err != nil {
		t.Fatal(err)
	}
	if err := RepairDescriptions(ctx, db); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT description FROM skills WHERE name = 'web-x'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "watch a page" {
		t.Errorf("repaired description = %q, want %q", got, "watch a page")
	}
	if err := db.QueryRow(`SELECT description FROM skills WHERE name = 'ok'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "kept" {
		t.Errorf("untouched description = %q, want %q", got, "kept")
	}
}

func TestEscapeLike(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"plain":      "plain",
		"web_fetch":  `web\_fetch`,
		"100%done":   `100\%done`,
		`back\slash`: `back\\slash`,
	}
	for in, want := range cases {
		if got := escapeLike(in); got != want {
			t.Errorf("escapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}
