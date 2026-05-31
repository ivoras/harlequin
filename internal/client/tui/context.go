package tui

import (
	"fmt"
	"image/color"
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

func (m *Model) headerZoneBG() color.Color {
	if m.modelThinking() {
		return lipgloss.Color(thinkingPulseColor(time.Now()))
	}
	return colorHeaderLine
}

func (m *Model) renderHeaderLine() string {
	left := m.styles.Header.Render(" Harlequin ")
	bg := m.headerZoneBG()
	thinking := m.renderHeaderThinking(bg)
	right := m.renderContextMeter(bg)
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
	gapFill := lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", gap))
	rest := thinking + gapFill + right
	if w := lipgloss.Width(rest); w < zoneW {
		rest += lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", zoneW-w))
	}
	return left + rest
}

func (m *Model) renderHeaderThinking(bg color.Color) string {
	if !m.loading {
		return ""
	}
	return m.styles.Status.Background(bg).Render(m.spin.View() + " thinking…  (Esc to cancel)")
}

func (m *Model) renderContextMeter(bg color.Color) string {
	if m.ctxMeter.max <= 0 {
		return m.styles.ContextMuted.Background(bg).Render("ctx —")
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
	bar := renderContextBar(pct, bg)
	model := truncateModelName(m.ctxMeter.model)
	label := fmt.Sprintf("%s  %s/%s", bar, used, max)
	if model != "" {
		label = model + " · " + label
	}
	style := m.styles.ContextOK.Background(bg)
	switch {
	case pct >= 90:
		style = m.styles.ContextCritical.Background(bg)
	case pct >= 70:
		style = m.styles.ContextWarn.Background(bg)
	}
	return style.Render(label)
}

func renderContextBar(pct int, bg color.Color) string {
	const slots = 8
	filled := pct * slots / 100
	if filled > slots {
		filled = slots
	}
	var sb strings.Builder
	fg := lipgloss.NewStyle().Foreground(colorAccent).Background(bg)
	dim := lipgloss.NewStyle().Foreground(colorBorder).Background(bg)
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
