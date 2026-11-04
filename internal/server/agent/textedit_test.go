package agent

import (
	"strings"
	"testing"
)

func TestTextEditReplace(t *testing.T) {
	out, n, err := textEditReplace("a b a", "b", "X", false)
	if err != nil || out != "a X a" || n != 1 {
		t.Fatalf("got %q %d %v", out, n, err)
	}
	if _, _, err := textEditReplace("a b a", "a", "X", false); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Fatalf("want ambiguity error, got %v", err)
	}
	out, n, _ = textEditReplace("a b a", "a", "X", true)
	if out != "X b X" || n != 2 {
		t.Fatalf("replace_all: %q %d", out, n)
	}
	for _, c := range [][2]string{{"zzz", "q"}, {"", "q"}, {"a", "a"}} {
		if _, _, err := textEditReplace("a b a", c[0], c[1], false); err == nil {
			t.Errorf("expected error for old=%q new=%q", c[0], c[1])
		}
	}
}

func TestTextEditAppend(t *testing.T) {
	if got := textEditAppend("one", "two"); got != "one\ntwo" {
		t.Errorf("got %q", got)
	}
	if got := textEditAppend("one\n", "two\n"); got != "one\ntwo\n" {
		t.Errorf("got %q", got)
	}
	if got := textEditAppend("", "two"); got != "two" {
		t.Errorf("got %q", got)
	}
}

const base = "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\n"

func TestTextEditApplyDiff(t *testing.T) {
	diff := `--- a/f
+++ b/f
@@ -2,3 +2,3 @@
 line2
-line3
+LINE3
 line4
@@ -7,2 +7,3 @@
 line7
 line8
+line9
`
	out, n, err := textEditApplyDiff(base, diff)
	if err != nil || n != 2 {
		t.Fatalf("%v %d", err, n)
	}
	want := "line1\nline2\nLINE3\nline4\nline5\nline6\nline7\nline8\nline9\n"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}

	// Wrong line number in the header: located by content.
	shifted := "@@ -40,3 +40,3 @@\n line2\n-line3\n+LINE3\n line4\n"
	if out, _, err := textEditApplyDiff(base, shifted); err != nil || !strings.Contains(out, "LINE3") {
		t.Fatalf("shifted diff should still apply: %v %q", err, out)
	}

	// Header without numbers, blank separator lines between hunks, no trailing newline.
	bare := "@@\n line2\n-line3\n+LINE3\n\n@@\n line7\n-line8\n+L8"
	out, _, err = textEditApplyDiff(base, bare)
	if err != nil || !strings.Contains(out, "LINE3") || !strings.Contains(out, "line7\nL8\n") {
		t.Fatalf("bare headers: %v %q", err, out)
	}

	// Mismatching context: nothing applied, error names the hunk.
	if _, _, err := textEditApplyDiff(base, "@@ -1,2 +1,2 @@\n line1\n-NOPE\n+x\n"); err == nil || !strings.Contains(err.Error(), "hunk 1") {
		t.Fatalf("expected hunk mismatch error, got %v", err)
	}
	// All-or-nothing: second hunk bad -> error, caller keeps the original.
	if _, _, err := textEditApplyDiff(base, "@@ -1,1 +1,1 @@\n-line1\n+X\n@@ -5,1 +5,1 @@\n-bad\n+Y\n"); err == nil {
		t.Fatal("expected error")
	}
	// Ambiguous hunk without a position.
	dup := "x\ny\nx\ny\n"
	if _, _, err := textEditApplyDiff(dup, "@@\n-x\n+z\n"); err == nil || !strings.Contains(err.Error(), "several") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	// ...but a correct hint picks the nearest.
	if out, _, err := textEditApplyDiff(dup, "@@ -3,1 +3,1 @@\n-x\n+z\n"); err != nil || out != "x\ny\nz\ny\n" {
		t.Fatalf("hinted: %v %q", err, out)
	}
	// Pure insertion after line 2, and into an empty file.
	if out, _, err := textEditApplyDiff("a\nb\nc\n", "@@ -2,0 +3,1 @@\n+ins\n"); err != nil || out != "a\nb\nins\nc\n" {
		t.Fatalf("insertion: %v %q", err, out)
	}
	if out, _, err := textEditApplyDiff("", "@@ -0,0 +1,2 @@\n+a\n+b\n"); err != nil || out != "a\nb\n" {
		t.Fatalf("empty file: %v %q", err, out)
	}
	// No hunks.
	if _, _, err := textEditApplyDiff(base, "just text"); err == nil {
		t.Fatal("expected no-hunks error")
	}
}

func TestParseTextEditOp(t *testing.T) {
	if _, err := parseTextEditOp(map[string]any{}); err == nil {
		t.Error("no op should fail")
	}
	if _, err := parseTextEditOp(map[string]any{"append": "x", "diff": "@@\n+y"}); err == nil {
		t.Error("two ops should fail")
	}
	op, err := parseTextEditOp(map[string]any{"old_string": "ban", "new_string": ""})
	if err != nil {
		t.Fatal(err)
	}
	if out, _, err := op.apply("banana"); err != nil || out != "ana" {
		t.Errorf("deleting via empty new_string: %q %v", out, err)
	}
}
