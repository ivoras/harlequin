package tui

import (
	"strings"
	"testing"
)

func TestRenderMarkdownBold(t *testing.T) {
	t.Parallel()
	out := renderMarkdown(80, "**Existing memory (s.6):** hello")
	if out == "" {
		t.Fatal("empty output")
	}
	if strings.Contains(out, "**") {
		t.Fatalf("raw markdown leaked: %q", out)
	}
	if !strings.Contains(out, "Existing memory") {
		t.Fatalf("missing text: %q", out)
	}
}

func TestRenderMarkdownTable(t *testing.T) {
	t.Parallel()
	in := "| A | B |\n|---|---|\n| 1 | 2 |"
	out := renderMarkdown(80, in)
	if out == "" {
		t.Fatal("empty output")
	}
	if !strings.Contains(out, "1") || !strings.Contains(out, "2") {
		t.Fatalf("table cells missing: %q", out)
	}
}

func TestRenderMarkdownBlockquote(t *testing.T) {
	t.Parallel()
	out := renderMarkdown(80, "> quoted line")
	if !strings.Contains(out, "quoted") {
		t.Fatalf("missing quote text: %q", out)
	}
}
