package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// handleSessionsKey drives the interactive session picker (phaseSessions): ↑/↓ to
// move, enter to resume the highlighted session, esc/q to cancel back to chat.
func (m *Model) handleSessionsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k", "ctrl+p":
		if m.sessionSel > 0 {
			m.sessionSel--
		}
	case "down", "j", "ctrl+n":
		if m.sessionSel < len(m.sessionList)-1 {
			m.sessionSel++
		}
	case "enter":
		if m.sessionSel >= 0 && m.sessionSel < len(m.sessionList) {
			sel := m.sessionList[m.sessionSel]
			m.sessTitle = cleanSessionTitle(sel.Title)
			m.phase = phaseChat
			cmd := m.resumeSession(sel.ID)
			return m, tea.Batch(m.input.Focus(), cmd)
		}
	case "esc", "q":
		m.phase = phaseChat
		return m, m.input.Focus()
	}
	return m, nil
}

// cleanSessionTitle blanks the generic placeholder titles for the header.
func cleanSessionTitle(t string) string {
	switch t {
	case "Session", "New session", "New conversation":
		return ""
	}
	return t
}

// sessionsView renders the session picker.
func (m *Model) sessionsView() string {
	var sb strings.Builder
	sb.WriteString(m.styles.Header.Render(" Resume a session ") + "\n\n")
	sb.WriteString(m.styles.Help.Render("↑/↓: move · enter: resume · esc: cancel") + "\n\n")
	for i, s := range m.sessionList {
		title := s.Title
		if strings.TrimSpace(title) == "" {
			title = "(untitled)"
		}
		line := fmt.Sprintf("#%d  %s  · %s", s.ID, title, s.UpdatedAt.Format("2006-01-02 15:04"))
		if i == m.sessionSel {
			sb.WriteString(m.styles.Accent.Render("▸ "+line) + "\n")
		} else {
			sb.WriteString("  " + line + "\n")
		}
	}
	return sb.String()
}
