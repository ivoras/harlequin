package tui

import "testing"

func TestParseMCPRef(t *testing.T) {
	for in, want := range map[string][2]string{
		"foo":              {"user", "foo"},
		"user/foo":         {"user", "foo"},
		"personal/foo":     {"user", "foo"},
		"shared/foo":       {"shared", "foo"},
		"Organisation/foo": {"shared", "foo"},
		"organization/foo": {"shared", "foo"},
		"weird/foo":        {"user", "weird/foo"},
	} {
		if sc, n := parseMCPRef(in); sc != want[0] || n != want[1] {
			t.Errorf("%q = %s/%s, want %v", in, sc, n, want)
		}
	}
}
