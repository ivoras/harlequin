package tui

import "testing"

func TestWithAttachments(t *testing.T) {
	m := &Model{}
	if got := m.withAttachments("hi"); got != "hi" {
		t.Fatalf("no attachments must leave the text alone, got %q", got)
	}
	m.attachments = []attachment{{ref: attachRef("personal", 0, 18), name: "a b.pdf"}, {ref: attachRef("project", 3, 19), name: `we"ird.md`}}
	want := "look\n\nAttached documents:\n@\"u.18/a b.pdf\"\n@\"p3.19/weird.md\""
	if got := m.withAttachments("look"); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if len(m.attachments) != 0 {
		t.Error("attachments must clear after being listed")
	}
	if attachRef("shared", 0, 4) != "s.4" {
		t.Error("shared ref")
	}
}
