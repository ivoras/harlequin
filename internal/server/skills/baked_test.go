package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every baked skill must parse, and must come out with a non-empty description.
//
// This is not a style check. A YAML slip in a description — an unquoted "word: "
// is enough, since a plain scalar cannot contain a colon-space — makes
// parseFrontmatter fail, and descriptionOf answers a parse failure with the
// empty string rather than an error. The skill then seeds into the catalogue
// with no description at all, and the model can no longer route to it: the skill
// is installed, looks fine in a listing of names, and is never chosen. Losing a
// skill that way is silent in production, so it has to be loud here.
func TestBakedSkillsParse(t *testing.T) {
	const skillsDir = "../../../skills"
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatalf("read %s: %v", skillsDir, err)
	}

	seen := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue // system_prompt.md and friends
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			md, err := os.ReadFile(filepath.Join(skillsDir, name, "SKILL.md"))
			if err != nil {
				t.Fatalf("read SKILL.md: %v", err)
			}
			fm, body, err := parseFrontmatter(string(md))
			if err != nil {
				t.Fatalf("parse frontmatter: %v", err)
			}
			if strings.TrimSpace(fm.Description) == "" {
				t.Error("description is empty: the model cannot route to this skill")
			}
			if strings.TrimSpace(body) == "" {
				t.Error("body is empty")
			}
			// descriptionOf is the path the seeder actually uses, and it reports
			// a parse failure as "" — so check it agrees rather than trusting the
			// direct parse above.
			if got := descriptionOf(string(md)); got != fm.Description {
				t.Errorf("descriptionOf disagrees with parseFrontmatter (%q vs %q)", got, fm.Description)
			}
		})
		seen++
	}
	if seen == 0 {
		t.Fatalf("no skill directories found under %s", skillsDir)
	}
}

// Every baked hat must parse with a description, and any skill it names must exist.
func TestBakedHatsParse(t *testing.T) {
	const hatsDir = "../../../hats"
	entries, err := os.ReadDir(hatsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		md, err := os.ReadFile(filepath.Join(hatsDir, e.Name(), hatPromptFile))
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		h := hatFromPrompt(e.Name(), string(md))
		if strings.TrimSpace(h.Description) == "" {
			t.Errorf("hat %s: empty description", e.Name())
		}
		for _, s := range h.Skills {
			if _, err := os.Stat(filepath.Join("../../../skills", s, "SKILL.md")); err != nil {
				t.Errorf("hat %s names missing skill %q", e.Name(), s)
			}
		}
	}
	// The prompts name skills in backticks; those must exist too.
	for _, hat := range []string{"analyst", "researcher"} {
		md, _ := os.ReadFile(filepath.Join(hatsDir, hat, hatPromptFile))
		for _, f := range strings.Fields(string(md)) {
			if n := strings.Trim(f, "`,.;"); strings.HasPrefix(f, "`") && strings.ContainsAny(n, "-_") {
				if _, err := os.Stat(filepath.Join("../../../skills", n, "SKILL.md")); err != nil {
					t.Errorf("hat %s mentions missing skill %q", hat, n)
				}
			}
		}
	}
}
