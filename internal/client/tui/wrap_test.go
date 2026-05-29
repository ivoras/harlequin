package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestWrapWidth(t *testing.T) {
	t.Parallel()
	in := "one two three four five six seven"
	got := wrapWidth(10, in)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrap, got %q", got)
	}
	for _, line := range lines {
		if lipgloss.Width(line) > 10 {
			t.Fatalf("line too wide (%d): %q", lipgloss.Width(line), line)
		}
	}
}

func TestWrapWidthPreservesANSI(t *testing.T) {
	t.Parallel()
	style := lipgloss.NewStyle().Bold(true)
	in := style.Render("hello hello hello hello")
	got := wrapWidth(8, in)
	if !strings.Contains(got, "\n") {
		t.Fatalf("expected wrapped styled text, got %q", got)
	}
}

func TestContentWidthUsesViewport(t *testing.T) {
	t.Parallel()
	m := &Model{width: 120, phase: phaseChat}
	m.vp.SetWidth(80)
	if w := m.contentWidth(); w != 80 {
		t.Fatalf("contentWidth=%d want 80", w)
	}
}
