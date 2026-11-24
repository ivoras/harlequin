package agent

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/ivoras/harlequin/internal/server/skills"
)

// isSkillScript: files a skill runs rather than the model reads — its tools,
// run_js script:, include() and cron targets use them by path.
func isSkillScript(rel string) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".js", ".mjs", ".cjs":
		return true
	}
	return false
}

// skillLoadText is what load_skill returns: SKILL.md first, then the other
// files in path order, in full except scripts, which are only listed (a
// skill's library code can be most of its size and is never needed to follow
// the instructions; read_doc reads one on demand).
func skillLoadText(sk *skills.Skill) string {
	var rels []string
	for rel := range sk.Files {
		if rel != "SKILL.md" {
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	var sb strings.Builder
	if md, ok := sk.Files["SKILL.md"]; ok {
		fmt.Fprintf(&sb, "=== SKILL.md ===\n%s\n", md)
	}
	var scripts []string
	for _, rel := range rels {
		if isSkillScript(rel) {
			scripts = append(scripts, fmt.Sprintf("skill://%s/%s (%d KB)", sk.Name, rel, (len(sk.Files[rel])+1023)/1024))
			continue
		}
		fmt.Fprintf(&sb, "=== %s ===\n%s\n", rel, sk.Files[rel])
	}
	if len(scripts) > 0 {
		fmt.Fprintf(&sb, "=== scripts (not shown) ===\n%s\nThey run through the skill's own tools, run_js (script: \"skill://…\", or include(\"skill://…\") in code) and cron jobs; you do not need their code. To read one anyway, read_doc it by its skill:// path.\n", strings.Join(scripts, "\n"))
	}
	// Point of use: the model looks here, not in the tool list, for how to change a skill.
	fmt.Fprintf(&sb, "=== to change this skill (only when the user asks) ===\nread_doc \"skill://%s/SKILL.md\" (or another file) shows its source; change it with edit_doc on that path, or replace a whole file with write_file.\n", sk.Name)
	return sb.String()
}
