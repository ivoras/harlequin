package agent

import (
	"context"
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
	return sb.String()
}

// skillFileText reads one file of a skill by its skill://<skill>/<path> URI,
// resolved like load_skill (hat overlay, project/shared/personal scope).
func (a *Agent) skillFileText(ctx context.Context, rc *runContext, uri string) (string, string) {
	name, rel, ok := strings.Cut(strings.TrimPrefix(uri, "skill://"), "/")
	if !ok || name == "" || rel == "" {
		return "", `error: a skill file is named skill://<skill>/<path>, e.g. "skill://e-racun/reference/codes.md"`
	}
	if a.Skills == nil {
		return "", "error: skills are not available here"
	}
	sk, err := a.Skills.ResolveEffective(ctx, rc.userDB, rc.projectDB, name, rc.userID, rc.username, rc.hat)
	if err != nil {
		return "", fmt.Sprintf("error: no skill %q: %v", name, err)
	}
	text, ok := sk.Files[rel]
	if !ok {
		var have []string
		for r := range sk.Files {
			have = append(have, r)
		}
		sort.Strings(have)
		return "", fmt.Sprintf("error: skill %q has no file %q; its files are: %s", name, rel, strings.Join(have, ", "))
	}
	return text, ""
}
