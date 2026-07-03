package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The baked compare skills must parse: a YAML frontmatter slip would make
// buildSkill error and the skill silently vanish from the catalogue. Their
// descriptions must also cross-reference each other so the model can route
// versions-of-one-text vs two-different-texts correctly.
func TestCompareSkillsParse(t *testing.T) {
	crossRef := map[string]string{
		"compare-versions": "compare-topics",
		"compare-topics":   "compare-versions",
	}
	for name, other := range crossRef {
		root := filepath.Join("..", "..", "..", "skills", name)
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
			t.Fatalf("walk %s: %v", name, err)
		}
		sk, err := buildSkill(name, files, "deployed")
		if err != nil {
			t.Fatalf("buildSkill %s: %v", name, err)
		}
		if sk.Name != name {
			t.Fatalf("%s: name = %q", name, sk.Name)
		}
		if sk.Description == "" {
			t.Fatalf("%s: description is empty (frontmatter likely mis-parsed)", name)
		}
		if !strings.Contains(sk.Description, other) {
			t.Errorf("%s: description should point mis-routed requests at %s", name, other)
		}
		if !strings.Contains(files["SKILL.md"], "align_docs") {
			t.Errorf("%s: SKILL.md never mentions the align_docs tool", name)
		}
	}
}
