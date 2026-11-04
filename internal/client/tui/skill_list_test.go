package tui

import (
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestRenderSkillListFlagsHatOverride(t *testing.T) {
	infos := []types.SkillInfo{
		{Name: "report", Source: "shared", Description: "d"},
		{Name: "other", Source: "user", Description: "d"},
	}
	out := renderSkillList(infos, "analyst", []string{"report"})
	if !strings.Contains(out, "report [shared, overridden by hat analyst]") {
		t.Fatalf("missing override flag:\n%s", out)
	}
	if strings.Contains(out, "other [user, overridden") {
		t.Fatalf("flagged a skill the hat doesn't carry:\n%s", out)
	}
}
