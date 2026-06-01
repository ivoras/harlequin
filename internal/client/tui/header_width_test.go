package tui

import (
	"testing"

	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"
)

func TestHeaderLineWidth(t *testing.T) {
	for _, w := range []int{40, 80, 120, 200} {
		for _, loading := range []bool{false, true} {
			for _, meter := range []bool{false, true} {
				m := &Model{styles: newStyles(), width: w, loading: loading}
				m.spin = spinner.New()
				m.spin.Spinner = spinner.Dot
				if meter {
					m.ctxMeter = contextMeterState{model: "anthropic/claude-opus-4-8", used: 12345, max: 200000}
				}
				line := m.renderHeaderLine()
				got := lipgloss.Width(line)
				if got != w {
					t.Errorf("width=%d loading=%v meter=%v -> rendered width %d (want %d)", w, loading, meter, got, w)
				}
			}
		}
	}
}
