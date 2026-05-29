package tui

import "strings"

const maxInputHistory = 200

// pushInputHistory records a submitted input line for up/down recall.
func (m *Model) pushInputHistory(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	n := len(m.inputHistory)
	if n > 0 && m.inputHistory[n-1] == line {
		m.resetInputHistoryNav()
		return
	}
	m.inputHistory = append(m.inputHistory, line)
	if len(m.inputHistory) > maxInputHistory {
		m.inputHistory = m.inputHistory[len(m.inputHistory)-maxInputHistory:]
	}
	m.resetInputHistoryNav()
}

func (m *Model) resetInputHistoryNav() {
	m.historyIndex = len(m.inputHistory)
	m.historyDraft = ""
}

// tryRecallHistory handles up/down when the user is recalling prior input.
// Returns true if the key was consumed.
func (m *Model) tryRecallHistory(delta int) bool {
	if len(m.inputHistory) == 0 {
		return false
	}
	if !m.shouldUseInputHistory(delta) {
		return false
	}

	if m.historyIndex == len(m.inputHistory) {
		m.historyDraft = m.input.Value()
	}

	next := m.historyIndex + delta
	if next < 0 {
		next = 0
	}
	if next > len(m.inputHistory) {
		next = len(m.inputHistory)
	}
	m.historyIndex = next

	switch {
	case next == len(m.inputHistory):
		m.input.SetValue(m.historyDraft)
	default:
		m.input.SetValue(m.inputHistory[next])
	}
	m.input.CursorEnd()
	return true
}

func (m *Model) shouldUseInputHistory(delta int) bool {
	if m.historyIndex < len(m.inputHistory) {
		return true
	}
	if strings.Contains(m.input.Value(), "\n") {
		return false
	}
	return true
}
