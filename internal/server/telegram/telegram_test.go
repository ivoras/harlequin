package telegram

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestSplit(t *testing.T) {
	text := strings.Repeat("a", 8) + "\n" + strings.Repeat("😀", 6) + strings.Repeat("b", 9)
	parts := Split(text, 10)
	if parts[0] != "aaaaaaaa" { // breaks at the newline, not mid-line
		t.Fatalf("first part = %q", parts[0])
	}
	for _, p := range parts {
		if n := len(utf16.Encode([]rune(p))); n > 10 || n == 0 {
			t.Fatalf("part %q is %d UTF-16 units", p, n)
		}
	}
	if got := strings.Join(parts, ""); got != strings.Replace(text, "\n", "", 1) {
		t.Fatalf("lost text: %q", got)
	}
	if Split("", 10) != nil {
		t.Fatal("empty text should give no parts")
	}
}
