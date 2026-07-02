package tui

import (
	"context"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// skillEditor is the state of the built-in skill-file editor overlay. It wraps a
// textarea (which supplies line numbers, current-line highlighting, cursor
// movement, and the usual insert/delete editing operations) with a header and a
// save/cancel footer.
type skillEditor struct {
	ta        textarea.Model
	name      string
	relpath   string
	scope     string // target scope for the save ("" = default)
	fromScope string // scope the file resolved from (for display)
	status    string
}

// skillEditorLoadedMsg carries a skill file fetched for editing.
type skillEditorLoadedMsg struct {
	name, relpath, scope, fromScope, content string
	err                                      error
}

// skillEditorSavedMsg is emitted after a save attempt.
type skillEditorSavedMsg struct {
	name, relpath string
	err           error
}

// openSkillEditor fetches the file, then opens the editor overlay.
func (m *Model) openSkillEditor(name, relpath, scope string) tea.Cmd {
	return func() tea.Msg {
		content, fromScope, err := m.client.GetSkillFile(context.Background(), name, relpath)
		return skillEditorLoadedMsg{name: name, relpath: relpath, scope: scope, fromScope: fromScope, content: content, err: err}
	}
}

// startSkillEditor initializes the editor overlay from a loaded file.
func (m *Model) startSkillEditor(msg skillEditorLoadedMsg) tea.Cmd {
	ta := textarea.New()
	ta.ShowLineNumbers = true
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.MaxHeight = 0 // unbounded; sized to the viewport in editorView
	ta.SetValue(msg.content)

	// Highlight the current line (a subtle surface background) to make the caret
	// row obvious, matching the request for line highlighting.
	st := ta.Styles()
	st.Focused.CursorLine = st.Focused.CursorLine.Background(colorSurface)
	st.Focused.CursorLineNumber = st.Focused.CursorLineNumber.Foreground(colorAccentHi).Background(colorSurface)
	st.Focused.LineNumber = st.Focused.LineNumber.Foreground(colorMuted)
	ta.SetStyles(st)

	m.editor = &skillEditor{
		ta:        ta,
		name:      msg.name,
		relpath:   msg.relpath,
		scope:     msg.scope,
		fromScope: msg.fromScope,
	}
	m.phase = phaseEditor
	return m.editor.ta.Focus()
}

// handleEditorKey drives the editor overlay: Ctrl-S saves, Esc cancels, and any
// other key edits the buffer via the textarea.
func (m *Model) handleEditorKey(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	if m.editor == nil {
		m.phase = phaseChat
		return m, m.input.Focus()
	}
	switch key {
	case "ctrl+s":
		return m, m.saveSkillEditor()
	case "esc":
		m.phase = phaseChat
		m.editor = nil
		return m, m.input.Focus()
	}
	var cmd tea.Cmd
	m.editor.ta, cmd = m.editor.ta.Update(msg)
	m.editor.status = ""
	return m, cmd
}

// saveSkillEditor uploads the buffer to the skill file in its target scope.
func (m *Model) saveSkillEditor() tea.Cmd {
	ed := m.editor
	if ed == nil {
		return nil
	}
	name, relpath, scope, content := ed.name, ed.relpath, ed.scope, ed.ta.Value()
	return func() tea.Msg {
		err := m.client.PutSkillFile(context.Background(), name, relpath, scope, content)
		return skillEditorSavedMsg{name: name, relpath: relpath, err: err}
	}
}

// editorView renders the editor overlay (header, sized textarea, footer).
func (m *Model) editorView() string {
	ed := m.editor
	if ed == nil {
		return ""
	}
	title := " edit skill://" + ed.name + "/" + ed.relpath + " "
	header := m.styles.Header.Render(title)

	target := ed.scope
	if target == "" {
		target = "default"
	}
	hint := "Ctrl-S save · Esc cancel · from: " + orDash(ed.fromScope) + " · save to: " + target
	if ed.status != "" {
		hint = ed.status + "  ·  " + hint
	}
	footer := m.styles.Help.Render(hint)

	bodyH := m.height - 4
	if bodyH < 3 {
		bodyH = 3
	}
	ed.ta.SetWidth(m.width)
	ed.ta.SetHeight(bodyH)
	return header + "\n" + ed.ta.View() + "\n" + footer
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
