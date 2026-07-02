package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// contextMeterState holds the latest context usage from the server (SSE done).
type contextMeterState struct {
	model string
	used  int
	max   int
}

func (m *Model) headerZoneBG() color.Color {
	if m.modelThinking() {
		return lipgloss.Color(thinkingPulseColor(time.Now()))
	}
	return colorHeaderLine
}

func (m *Model) renderHeaderLine() string {
	left := m.styles.Header.Render(" Harlequin ")
	// The whole zone background fades while the model is thinking (the spinner and
	// "Thinking…" label themselves live on the latest transcript entry now).
	bg := m.headerZoneBG()
	// Show the session title (set by the server's auto-titler) next to the brand,
	// budget-limited so the context meter on the right always fits.
	if t := strings.TrimSpace(m.sessTitle); t != "" {
		budget := m.width/2 - lipgloss.Width(left)
		if budget > 4 {
			label := " " + ansi.Truncate(t, budget, "…") + " "
			left += m.styles.ContextMuted.Background(bg).Render(label)
		}
	}
	// Persistent worn-hat indicator (take it off with /hat off).
	if m.currentHat != "" {
		budget := m.width*3/4 - lipgloss.Width(left)
		if budget > 6 {
			label := " 🎩 " + ansi.Truncate(m.currentHat, budget-5, "…") + " "
			left += m.styles.Accent.Background(bg).Render(label)
		}
	}
	leftW := lipgloss.Width(left)
	zoneW := m.width - leftW
	if zoneW < 1 {
		zoneW = 1
	}
	// The meter is right-aligned; truncate (rather than clamp the gap) so the line
	// never exceeds zoneW and wraps onto a second row.
	right := m.renderContextMeter(bg)
	if rightW := lipgloss.Width(right); rightW > zoneW {
		right = ansi.Truncate(right, zoneW, "")
	}
	gap := zoneW - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	gapFill := lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", gap))
	return left + gapFill + right
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
	style := m.styles.ContextOK
	switch {
	case pct >= 90:
		style = m.styles.ContextCritical
	case pct >= 70:
		style = m.styles.ContextWarn
	}
	// Style each segment independently so every cell carries the background.
	// The bar self-styles each glyph; nesting it inside one outer Background
	// style would let the bar's internal resets strip the bg from the text
	// that follows (leaving the token count on the terminal's default bg).
	seg := func(s string) string { return style.Background(bg).Render(s) }
	bar := renderContextBar(pct, bg)
	out := bar + seg(fmt.Sprintf("  %s/%s", used, max))
	if model := truncateModelName(m.ctxMeter.model); model != "" {
		out = seg(model+" · ") + out
	}
	return out
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
