package tui

import "testing"

func TestFormatTokenCount(t *testing.T) {
	t.Parallel()
	if got := formatTokenCount(128500); got != "128.5k" {
		t.Fatalf("got %q", got)
	}
	if got := formatTokenCount(900); got != "900" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderContextMeter(t *testing.T) {
	t.Parallel()
	m := &Model{styles: newStyles(), width: 100, ctxMeter: contextMeterState{
		model: "openai/gpt-4o-mini", used: 45000, max: 128000,
	}}
	out := m.renderContextMeter()
	if out == "" || out == "ctx —" {
		t.Fatalf("got %q", out)
	}
}
