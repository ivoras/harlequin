package agent

import "testing"

func TestAttachedRef(t *testing.T) {
	for in, want := range map[string]string{
		`@"u.18/report.pdf"`:   "u.18",
		`@"p3.19/my notes.md"`: "p3.19",
		` s.4 `:                "s.4",
		"7":                    "7",
	} {
		if got := attachedRef(in); got != want {
			t.Errorf("attachedRef(%q) = %q, want %q", in, got, want)
		}
	}
}
