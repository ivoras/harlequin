package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestEnterSends(t *testing.T) {
	t.Parallel()
	plain := tea.KeyPressMsg{Code: tea.KeyEnter}
	shift := tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
	alt := tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
	ctrl := tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}

	if !enterSends(plain) {
		t.Fatal("plain enter should send")
	}
	if enterSends(shift) {
		t.Fatal("shift+enter should not send")
	}
	if enterSends(alt) {
		t.Fatal("alt+enter should not send")
	}
	if !enterSends(ctrl) {
		t.Fatal("ctrl+enter should send")
	}
}
