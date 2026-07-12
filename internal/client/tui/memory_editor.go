package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// memorySlotsMarker separates a memory's content from its editable slots in
// the single-textarea buffer used by the memory editor overlay.
const memorySlotsMarker = "--- slots (key: value per line; blank lines ignored) ---"

// memoryEditor is the state of the built-in memory editor overlay (phaseMemoryEditor).
type memoryEditor struct {
	ta        textarea.Model
	id        string
	scope     string // "user" | "shared" | "project"
	projectID int64
	status    string
}

// memoryEditorLoadedMsg carries a memory fetched for editing.
type memoryEditorLoadedMsg struct {
	mem       *types.Memory
	projectID int64
	err       error
}

// memoryEditorSavedMsg is emitted after a save attempt.
type memoryEditorSavedMsg struct {
	id  string
	err error
}

// openMemoryEditor fetches a memory (project-scoped ids are resolved via the
// project's memory list, since project memories live in a separate database
// not reachable through GetMemory), then opens the editor overlay on it.
func (m *Model) openMemoryEditor(id string, projectID int64) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if strings.HasPrefix(id, "p.") {
			if projectID == 0 {
				return memoryEditorLoadedMsg{err: fmt.Errorf("no active project — switch with /project use <name> first")}
			}
			mems, err := m.client.ListProjectMemory(ctx, projectID)
			if err != nil {
				return memoryEditorLoadedMsg{err: err}
			}
			for i := range mems {
				if mems[i].ID == id {
					return memoryEditorLoadedMsg{mem: &mems[i], projectID: projectID}
				}
			}
			return memoryEditorLoadedMsg{err: fmt.Errorf("memory %s not found", id)}
		}
		mem, err := m.client.GetMemory(ctx, id)
		return memoryEditorLoadedMsg{mem: mem, err: err}
	}
}

// startMemoryEditor initializes the editor overlay from a loaded memory. The
// buffer is the memory content, plus (for user/shared scope) a marker line
// followed by one "key: value" line per slot.
func (m *Model) startMemoryEditor(msg memoryEditorLoadedMsg) tea.Cmd {
	mem := msg.mem
	var buf strings.Builder
	buf.WriteString(mem.Content)
	if mem.Scope != "project" {
		buf.WriteString("\n\n")
		buf.WriteString(memorySlotsMarker)
		buf.WriteString("\n")
		for _, s := range mem.Slots {
			buf.WriteString(s.Key)
			buf.WriteString(": ")
			buf.WriteString(s.Value)
			buf.WriteString("\n")
		}
	}

	ta := textarea.New()
	ta.ShowLineNumbers = true
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.MaxHeight = 0 // unbounded; sized to the viewport in memoryEditorView
	ta.SetValue(buf.String())

	st := ta.Styles()
	st.Focused.CursorLine = st.Focused.CursorLine.Background(colorSurface)
	st.Focused.CursorLineNumber = st.Focused.CursorLineNumber.Foreground(colorAccentHi).Background(colorSurface)
	st.Focused.LineNumber = st.Focused.LineNumber.Foreground(colorMuted)
	ta.SetStyles(st)

	m.memEditor = &memoryEditor{
		ta:        ta,
		id:        mem.ID,
		scope:     mem.Scope,
		projectID: msg.projectID,
	}
	m.phase = phaseMemoryEditor
	return m.memEditor.ta.Focus()
}

// handleMemoryEditorKey drives the memory editor overlay: Ctrl-S saves, Esc
// cancels, any other key edits the buffer via the textarea.
func (m *Model) handleMemoryEditorKey(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	ed := m.memEditor
	if ed == nil {
		m.phase = phaseChat
		return m, m.input.Focus()
	}
	switch key {
	case "ctrl+s":
		return m, m.saveMemoryEditor()
	case "esc":
		m.phase = phaseChat
		m.memEditor = nil
		return m, m.input.Focus()
	}
	var cmd tea.Cmd
	ed.ta, cmd = ed.ta.Update(msg)
	ed.status = ""
	return m, cmd
}

// parseMemoryEditorBuffer splits the buffer at memorySlotsMarker into content
// and, if the marker is present, the parsed "key: value" slot lines.
func parseMemoryEditorBuffer(buf string) (content string, slots []types.MemorySlot, hasSlots bool) {
	idx := strings.Index(buf, memorySlotsMarker)
	if idx < 0 {
		return strings.TrimSpace(buf), nil, false
	}
	content = strings.TrimSpace(buf[:idx])
	rest := buf[idx+len(memorySlotsMarker):]
	slots = []types.MemorySlot{}
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		key := strings.TrimSpace(parts[0])
		if key == "" {
			continue
		}
		value := ""
		if len(parts) == 2 {
			value = strings.TrimSpace(parts[1])
		}
		slots = append(slots, types.MemorySlot{Key: key, Value: value})
	}
	return content, slots, true
}

// saveMemoryEditor uploads the buffer as a content(+slots) update.
func (m *Model) saveMemoryEditor() tea.Cmd {
	ed := m.memEditor
	if ed == nil {
		return nil
	}
	id, scope, projectID := ed.id, ed.scope, ed.projectID
	content, slots, hasSlots := parseMemoryEditorBuffer(ed.ta.Value())
	if content == "" {
		ed.status = "content cannot be empty"
		return nil
	}
	req := types.UpdateMemoryRequest{Content: &content}
	if hasSlots && scope != "project" {
		req.Slots = &slots
	}
	return func() tea.Msg {
		_, err := m.client.UpdateMemory(context.Background(), id, req, projectID)
		return memoryEditorSavedMsg{id: id, err: err}
	}
}

// memoryEditorView renders the memory editor overlay (header, sized textarea, footer).
func (m *Model) memoryEditorView() string {
	ed := m.memEditor
	if ed == nil {
		return ""
	}
	header := m.styles.Header.Render(" edit memory://" + ed.id + " ")

	hint := "Ctrl-S save · Esc cancel"
	if ed.scope == "project" {
		hint += " · project memory (no slots)"
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
