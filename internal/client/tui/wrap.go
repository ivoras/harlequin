package tui

import (
	"charm.land/lipgloss/v2"
)

// contentWidth is the usable terminal width for transcript text.
func (m *Model) contentWidth() int {
	w := m.width
	if m.phase == phaseChat {
		if vw := m.vp.Width(); vw > 0 {
			w = vw
		}
	}
	return w
}

// wrapWidth word-wraps s to width, preserving ANSI styles when present.
func wrapWidth(width int, s string) string {
	if width <= 0 || s == "" {
		return s
	}
	return lipgloss.Wrap(s, width, " ")
}

// wrapStyled renders with style then wraps to the current content width.
func (m *Model) wrapStyled(style lipgloss.Style, s string) string {
	if s == "" {
		return ""
	}
	return wrapWidth(m.contentWidth(), style.Render(s))
}
