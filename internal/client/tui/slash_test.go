package tui

import (
	"reflect"
	"testing"
)

func TestMatchSlashCommands(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"hello", nil},          // no leading slash
		{"a /hat", nil},         // slash not at char 1
		{"/hat wear x", nil},    // has a space -> typing args, menu closed
		{"/", slashCommands},    // all commands
		{"/re", []string{"/reload", "/resume"}},
		{"/RE", []string{"/reload", "/resume"}}, // case-insensitive
		{"/reload", []string{"/reload"}},
		{"/zzz", nil},
		{"/skill", []string{"/skill", "/skills"}},
	}
	for _, tc := range tests {
		if got := matchSlashCommands(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("matchSlashCommands(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsExactSlashCommand(t *testing.T) {
	t.Parallel()
	for _, c := range []string{"/hat", "/reload", "/quit", "/exit"} {
		if !isExactSlashCommand(c) {
			t.Errorf("%q should be exact", c)
		}
	}
	for _, c := range []string{"/re", "/ha", "/", "hat", ""} {
		if isExactSlashCommand(c) {
			t.Errorf("%q should not be exact", c)
		}
	}
}

func TestOverlayBottomLines(t *testing.T) {
	t.Parallel()
	view := "a\nb\nc\nd"
	got := overlayBottomLines(view, []string{"X", "Y"})
	if got != "a\nb\nX\nY" {
		t.Fatalf("got %q", got)
	}
	// Same number of lines as the input view (layout height preserved).
	if got := overlayBottomLines(view, nil); got != view {
		t.Fatalf("nil menu changed view: %q", got)
	}
	// Menu taller than view: clamps, never grows the line count (stays 2).
	out := overlayBottomLines("a\nb", []string{"X", "Y", "Z"})
	if n := len(splitLines(out)); n != 2 {
		t.Fatalf("line count changed to %d (want 2)", n)
	}
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

func TestClampSlashSel(t *testing.T) {
	t.Parallel()
	cases := [][3]int{{-1, 5, 0}, {0, 5, 0}, {4, 5, 4}, {5, 5, 4}, {9, 5, 4}, {3, 0, 0}}
	for _, c := range cases {
		if got := clampSlashSel(c[0], c[1]); got != c[2] {
			t.Errorf("clampSlashSel(%d,%d)=%d want %d", c[0], c[1], got, c[2])
		}
	}
}
