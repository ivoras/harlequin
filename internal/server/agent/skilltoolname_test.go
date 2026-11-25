package agent

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/skills"
)

// Skill tools are offered under names every provider accepts; the old dotted
// name stays callable but hidden.
func TestSkillToolNames(t *testing.T) {
	valid := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	reg := map[string]toolEntry{}
	a := &Agent{}
	a.registerSkillTool(reg, "e-racun", skills.ToolDefinition{Name: "check"})
	a.registerSkillTool(reg, "naplata ios.v2", skills.ToolDefinition{Name: "usporedi"})
	a.registerSkillTool(reg, strings.Repeat("dugo-ime-vjestine-", 5), skills.ToolDefinition{Name: "alat"})
	visible := 0
	for name, e := range reg {
		if e.hidden {
			continue
		}
		visible++
		if !valid.MatchString(name) || e.def.Function.Name != name {
			t.Errorf("offered tool name %q (def %q) is not valid for providers", name, e.def.Function.Name)
		}
	}
	if visible != 3 {
		t.Fatalf("visible tools = %d, want 3", visible)
	}
	if e, ok := reg["e-racun__check"]; !ok || e.hidden {
		t.Fatalf("e-racun__check missing or hidden: %v", ok)
	}
	if e, ok := reg["e-racun.check"]; !ok || !e.hidden {
		t.Fatalf("dotted alias missing or offered: %v", ok)
	}
	if _, ok := reg["naplata_ios_v2__usporedi"]; !ok {
		t.Fatalf("unsafe characters not replaced: %v", keys(reg))
	}
}

func keys(m map[string]toolEntry) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
