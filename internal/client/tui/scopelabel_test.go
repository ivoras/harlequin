package tui

import "testing"

func TestPersonalScopeNaming(t *testing.T) {
	for _, flag := range []string{"--personal", "--user", "-u"} {
		rest, scope := skillScopeFlag([]string{"foo", flag})
		if scope != "user" || len(rest) != 1 {
			t.Errorf("%s: scope %q rest %v", flag, scope, rest)
		}
	}
	if displayScope("user") != "personal" || displayScope("shared") != "shared" || displayScope("") != "" {
		t.Error("displayScope")
	}
}
