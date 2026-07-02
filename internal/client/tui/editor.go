package tui

import (
	"context"
	"slices"
	"strings"

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
	scope     string // target scope for the save ("" = ask / default)
	fromScope string // scope the file resolved from (for display + default target)
	status    string
	// picking is set while the save-scope prompt is shown (Ctrl-S with no
	// explicit scope and more than one writable scope).
	picking  bool
	writable []string
	// isHat: the buffer is a hat file (hats are shared-only, saved directly).
	isHat bool
}

// skillEditorLoadedMsg carries a skill file fetched for editing.
type skillEditorLoadedMsg struct {
	name, relpath, scope, fromScope, content string
	isHat                                    bool
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

// openHatEditor fetches a hat file, then opens the editor overlay on it. A
// system_prompt.md whose body is still empty is seeded with the default system
// prompt template, so a specialised prompt starts from the real default.
func (m *Model) openHatEditor(name, relpath string) tea.Cmd {
	return func() tea.Msg {
		content, err := m.client.GetHatFile(context.Background(), name, relpath)
		if err == nil && relpath == "system_prompt.md" && hatPromptBodyEmpty(content) {
			if tpl, terr := m.client.SystemPromptTemplate(context.Background()); terr == nil && tpl != "" {
				content = strings.TrimRight(content, "\n") + "\n" + tpl
			}
		}
		return skillEditorLoadedMsg{name: name, relpath: relpath, isHat: true, content: content, err: err}
	}
}

// hatPromptBodyEmpty reports whether a hat system_prompt.md has no body after
// its frontmatter (i.e. the hat still uses the default system prompt).
func hatPromptBodyEmpty(content string) bool {
	t := strings.TrimSpace(content)
	if !strings.HasPrefix(t, "---") {
		return t == ""
	}
	rest := t[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return false // malformed; do not seed over it
	}
	body := rest[idx+4:]
	body = strings.TrimPrefix(body, "-") // tolerate ---- fences
	return strings.TrimSpace(body) == ""
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
		isHat:     msg.isHat,
	}
	m.phase = phaseEditor
	return m.editor.ta.Focus()
}

// writableSkillScopes returns the scopes this user may save a skill into:
// user always, shared when elevated, project when one is active.
func (m *Model) writableSkillScopes() []string {
	scopes := []string{"user"}
	if m.canManageShared() {
		scopes = append(scopes, "shared")
	}
	if m.activeProjectID != 0 {
		scopes = append(scopes, "project")
	}
	return scopes
}

// handleEditorKey drives the editor overlay: Ctrl-S saves (asking for the
// target scope when more than one is writable), Esc cancels, and any other key
// edits the buffer via the textarea.
func (m *Model) handleEditorKey(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	ed := m.editor
	if ed == nil {
		m.phase = phaseChat
		return m, m.input.Focus()
	}
	if ed.picking {
		switch key {
		case "u":
			return m, m.saveSkillEditor("user")
		case "s":
			if hasScope(ed.writable, "shared") {
				return m, m.saveSkillEditor("shared")
			}
		case "p":
			if hasScope(ed.writable, "project") {
				return m, m.saveSkillEditor("project")
			}
		case "enter":
			return m, m.saveSkillEditor(ed.defaultScope())
		case "esc":
			ed.picking = false
			ed.status = ""
		}
		return m, nil
	}
	switch key {
	case "ctrl+s":
		if ed.isHat { // hats are shared-only: no scope to pick
			return m, m.saveSkillEditor("")
		}
		if ed.scope != "" { // explicit --scope flag on /skill edit: no prompt
			return m, m.saveSkillEditor(ed.scope)
		}
		ed.writable = m.writableSkillScopes()
		if len(ed.writable) == 1 {
			return m, m.saveSkillEditor(ed.writable[0])
		}
		ed.picking = true
		return m, nil
	case "esc":
		m.phase = phaseChat
		m.editor = nil
		return m, m.input.Focus()
	}
	var cmd tea.Cmd
	ed.ta, cmd = ed.ta.Update(msg)
	ed.status = ""
	return m, cmd
}

// defaultScope is the prompt's Enter choice: the scope the file resolved from
// when writable, else user.
func (ed *skillEditor) defaultScope() string {
	if hasScope(ed.writable, ed.fromScope) {
		return ed.fromScope
	}
	return "user"
}

func hasScope(scopes []string, s string) bool { return slices.Contains(scopes, s) }

// saveSkillEditor uploads the buffer to the skill file in the given scope.
func (m *Model) saveSkillEditor(scope string) tea.Cmd {
	ed := m.editor
	if ed == nil {
		return nil
	}
	ed.picking = false
	name, relpath, content, isHat := ed.name, ed.relpath, ed.ta.Value(), ed.isHat
	return func() tea.Msg {
		var err error
		if isHat {
			err = m.client.PutHatFile(context.Background(), name, relpath, content)
		} else {
			err = m.client.PutSkillFile(context.Background(), name, relpath, scope, content)
		}
		return skillEditorSavedMsg{name: name, relpath: relpath, err: err}
	}
}

// editorView renders the editor overlay (header, sized textarea, footer).
func (m *Model) editorView() string {
	ed := m.editor
	if ed == nil {
		return ""
	}
	scheme := "skill"
	if ed.isHat {
		scheme = "hat"
	}
	title := " edit " + scheme + "://" + ed.name + "/" + ed.relpath + " "
	header := m.styles.Header.Render(title)

	var hint string
	switch {
	case ed.picking:
		hint = "save to: (u)ser"
		if hasScope(ed.writable, "shared") {
			hint += " · (s)hared"
		}
		if hasScope(ed.writable, "project") {
			hint += " · (p)roject"
		}
		hint += " · Enter = " + ed.defaultScope() + " · Esc cancel"
	case ed.isHat:
		hint = "Ctrl-S save · Esc cancel · hat file (shared)"
	default:
		target := ed.scope
		if target == "" {
			target = "asks on save"
		}
		hint = "Ctrl-S save · Esc cancel · from: " + orDash(ed.fromScope) + " · save to: " + target
	}
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
