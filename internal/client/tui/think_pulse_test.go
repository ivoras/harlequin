package tui

import "testing"

func TestBlendRGB_endpoints(t *testing.T) {
	t.Parallel()
	y := blendRGB(thinkYellowRGB, thinkOrangeRGB, 0)
	o := blendRGB(thinkYellowRGB, thinkOrangeRGB, 1)
	if y != "#ffff00" {
		t.Fatalf("yellow: got %v", y)
	}
	if o != "#d78700" {
		t.Fatalf("orange: got %v", o)
	}
}

func TestModelThinking(t *testing.T) {
	t.Parallel()
	m := &Model{loading: true}
	if !m.modelThinking() {
		t.Fatal("expected thinking while loading")
	}
	m.loading = false
	if m.modelThinking() {
		t.Fatal("expected not thinking when idle")
	}
}
