package skills

import "testing"

func TestValidateRelpaths(t *testing.T) {
	t.Parallel()
	good := []string{"SKILL.md", "lib/x.js", "a/b/c.txt"}
	for _, rel := range good {
		if err := validateRelpaths(map[string]string{rel: ""}); err != nil {
			t.Errorf("%q rejected: %v", rel, err)
		}
	}
	bad := []string{"", "/etc/passwd", "../SKILL.md", "a/../../x", "a/./b", "a//b", `a\b`, ".."}
	for _, rel := range bad {
		if err := validateRelpaths(map[string]string{rel: ""}); err == nil {
			t.Errorf("%q accepted, want error", rel)
		}
	}
}

func TestEscapeLike(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"plain":      "plain",
		"web_fetch":  `web\_fetch`,
		"100%done":   `100\%done`,
		`back\slash`: `back\\slash`,
	}
	for in, want := range cases {
		if got := escapeLike(in); got != want {
			t.Errorf("escapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}
