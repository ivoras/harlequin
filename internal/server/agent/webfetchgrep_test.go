package agent

import (
	"regexp"
	"strings"
	"testing"
)

func TestGrepHTMLShortLineWhole(t *testing.T) {
	html := "<html>\n<span class=\"price\">148 500 JPY</span>\n<div>other</div>"
	got := grepHTML(html, regexp.MustCompile("(?i)price|JPY"))
	// The short matching line is returned whole (trimmed); "other" line excluded.
	if len(got) != 1 || got[0] != `<span class="price">148 500 JPY</span>` {
		t.Fatalf("got %#v", got)
	}
}

func TestGrepHTMLLongLineContext(t *testing.T) {
	// One long line (>120 chars) with the match in the middle.
	prefix := strings.Repeat("a", 90) + " "
	suffix := " " + strings.Repeat("z", 90)
	line := prefix + "TARGET" + suffix
	got := grepHTML(line, regexp.MustCompile("TARGET"))
	if len(got) != 1 {
		t.Fatalf("expected 1 snippet, got %#v", got)
	}
	s := got[0]
	if !strings.Contains(s, "TARGET") {
		t.Fatalf("snippet missing match: %q", s)
	}
	// Far shorter than the whole line; ellipses on both truncated ends.
	if len(s) >= len(line) {
		t.Fatalf("snippet not truncated: %d vs %d", len(s), len(line))
	}
	if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") {
		t.Fatalf("expected ellipses both ends: %q", s)
	}
	// Word-boundary trim: the run of 'a'/'z' is one word, so a partial leading/
	// trailing word is dropped entirely rather than shown cut.
	if strings.Contains(s, "a") || strings.Contains(s, "z") {
		t.Fatalf("partial word not trimmed: %q", s)
	}
}

func TestGrepHTMLDedupAndNoMatch(t *testing.T) {
	if got := grepHTML("<p>nothing here</p>", regexp.MustCompile("zzz")); len(got) != 0 {
		t.Fatalf("expected no matches, got %#v", got)
	}
	// Two identical short matching lines collapse to one.
	html := "<i>EUR</i>\n<i>EUR</i>"
	if got := grepHTML(html, regexp.MustCompile("(?i)eur")); len(got) != 1 {
		t.Fatalf("expected dedup to 1, got %#v", got)
	}
}

// The matched text is always shown in full even when context trimming bites.
func TestGrepHTMLMatchAlwaysWhole(t *testing.T) {
	line := strings.Repeat("x", 200) + "0.00542" + strings.Repeat("y", 200)
	got := grepHTML(line, regexp.MustCompile(`0\.00542`))
	if len(got) != 1 || !strings.Contains(got[0], "0.00542") {
		t.Fatalf("match not preserved whole: %#v", got)
	}
}
