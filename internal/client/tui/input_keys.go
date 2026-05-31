package tui

import tea "charm.land/bubbletea/v2"

// enterSends reports whether Enter should submit the input (plain Enter, or
// Enter with Ctrl). Shift+Enter and Alt+Enter are left to the textarea.
func enterSends(msg tea.KeyPressMsg) bool {
	k := msg.Key()
	if k.Code != tea.KeyEnter {
		return false
	}
	if k.Mod.Contains(tea.ModShift) || k.Mod.Contains(tea.ModAlt) {
		return false
	}
	return true
}
