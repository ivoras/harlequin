package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// contextMeterState holds the latest context usage from the server (SSE done).
type contextMeterState struct {
	model  string
	used   int
	max    int
}

func (m *Model) renderHeaderThinking() string {
	if !m.loading {
		return ""
	}
	return m.styles.Status.Render(m.spin.View() + " thinking…  (Esc to cancel)")
}

func (m *Model) renderHeaderLine() string {
	left := m.styles.Header.Render(" Harlequin ")
	thinking := m.renderHeaderThinking()
	right := m.renderContextMeter()
	leftW := lipgloss.Width(left)
	zoneW := m.width - leftW
	if zoneW < 1 {
		zoneW = 1
	}
	thinkingW := lipgloss.Width(thinking)
	rightW := lipgloss.Width(right)
	gap := zoneW - thinkingW - rightW
	if gap < 1 {
		gap = 1
	}
	rest := thinking + strings.Repeat(" ", gap) + right
	if m.modelThinking() {
		bg := lipgloss.Color(thinkingPulseColor(time.Now()))
		// Width fills the zone to the right edge of the window with background.
		rest = lipgloss.NewStyle().Width(zoneW).Background(bg).Render(rest)
	}
	return left + rest
}

func (m *Model) renderContextMeter() string {
	if m.ctxMeter.max <= 0 {
		return m.styles.ContextMuted.Render("ctx —")
	}
	used := formatTokenCount(m.ctxMeter.used)
	max := formatTokenCount(m.ctxMeter.max)
	pct := 0
	if m.ctxMeter.max > 0 {
		pct = m.ctxMeter.used * 100 / m.ctxMeter.max
		if pct > 100 {
			pct = 100
		}
	}
	bar := renderContextBar(pct)
	model := truncateModelName(m.ctxMeter.model)
	label := fmt.Sprintf("%s  %s/%s", bar, used, max)
	if model != "" {
		label = model + " · " + label
	}
	style := m.styles.ContextOK
	switch {
	case pct >= 90:
		style = m.styles.ContextCritical
	case pct >= 70:
		style = m.styles.ContextWarn
	}
	return style.Render(label)
}

func renderContextBar(pct int) string {
	const slots = 8
	filled := pct * slots / 100
	if filled > slots {
		filled = slots
	}
	var sb strings.Builder
	fg := lipgloss.NewStyle().Foreground(colorAccent)
	dim := lipgloss.NewStyle().Foreground(colorBorder)
	for i := 0; i < slots; i++ {
		if i < filled {
			sb.WriteString(fg.Render("▮"))
		} else {
			sb.WriteString(dim.Render("▯"))
		}
	}
	return sb.String()
}

func formatTokenCount(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func truncateModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	if len(model) > 18 {
		return model[:15] + "…"
	}
	return model
}
