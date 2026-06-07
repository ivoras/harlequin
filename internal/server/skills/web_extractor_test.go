package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// The baked web-extractor skill must parse: a YAML frontmatter slip would make
// buildSkill error and the skill silently vanish from the catalogue.
func TestWebExtractorSkillParses(t *testing.T) {
	root := filepath.Join("..", "..", "..", "skills", "web-extractor")
	files := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk skill: %v", err)
	}

	sk, err := buildSkill("web-extractor", files, "deployed")
	if err != nil {
		t.Fatalf("buildSkill: %v", err)
	}
	if sk.Name != "web-extractor" {
		t.Fatalf("name = %q", sk.Name)
	}
	if sk.Description == "" {
		t.Fatal("description is empty (frontmatter likely mis-parsed)")
	}
	for _, want := range []string{"SKILL.md", "lib/extract.js", "lib/check.js"} {
		if _, ok := files[want]; !ok {
			t.Errorf("missing skill file %q", want)
		}
	}
}
