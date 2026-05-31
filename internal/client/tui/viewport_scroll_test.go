package tui

import (
	"strings"
	"testing"

	clientcfg "github.com/ivoras/harlequin/internal/client/config"
)

func TestRefreshViewport_sticksToBottomWhenAtBottom(t *testing.T) {
	t.Parallel()
	m := &Model{cfg: &clientcfg.Config{}, styles: newStyles(), phase: phaseChat, width: 80}
	m.vp.SetWidth(80)
	m.vp.SetHeight(8)
	m.blocks = []roleBlock{{role: "info", text: strings.Repeat("line\n", 30)}}
	m.refreshViewport()
	if !m.vp.AtBottom() {
		t.Fatal("expected at bottom after refresh")
	}
}

func TestRefreshViewport_preservesOffsetWhenScrolledUp(t *testing.T) {
	t.Parallel()
	m := &Model{cfg: &clientcfg.Config{}, styles: newStyles(), phase: phaseChat, width: 80}
	m.vp.SetWidth(80)
	m.vp.SetHeight(8)
	m.blocks = []roleBlock{{role: "info", text: strings.Repeat("line\n", 30)}}
	m.refreshViewport()
	m.vp.PageUp()
	if m.vp.AtBottom() {
		t.Fatal("expected scrolled up")
	}
	yBefore := m.vp.YOffset()
	m.blocks = append(m.blocks, roleBlock{role: "info", text: "more"})
	m.refreshViewport()
	if m.vp.AtBottom() {
		t.Fatal("expected scroll position preserved while reading history")
	}
	if m.vp.YOffset() != yBefore {
		t.Fatalf("yOffset changed: %d -> %d", yBefore, m.vp.YOffset())
	}
}
