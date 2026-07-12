package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// layout sizes the viewport and input to the window.
func (m *Model) layout() {
	if m.width <= 0 {
		return
	}
	inputHeight := m.input.Height()
	if inputHeight < 1 {
		inputHeight = 1
	}
	// header(1) + alert box + input box(inputHeight+2 border) + help(1)
	chrome := 1 + m.alertLineCount() + inputHeight + 2 + 1
	vpHeight := m.height - chrome
	if vpHeight < 3 {
		vpHeight = 3
	}
	vpWidth := m.width
	if m.activeProjectID > 0 {
		// Reserve a right-hand column for the project chatroom side-pane.
		vpWidth = m.width - chatPaneWidth - 1
		if vpWidth < 20 {
			vpWidth = 20
		}
	}
	m.vp.SetWidth(vpWidth)
	m.vp.SetHeight(vpHeight)
	inputFrame := m.styles.InputBox.GetHorizontalFrameSize()
	inputW := m.width - inputFrame
	if inputW < 1 {
		inputW = 1
	}
	m.input.SetWidth(inputW)
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	var content string
	switch {
	case m.width == 0:
		content = "loading…"
	case m.phase == phaseLoginUser || m.phase == phaseLoginPass ||
		m.phase == phaseRegisterEmail || m.phase == phaseRegisterPass || m.phase == phaseRegisterCode:
		content = m.loginView()
	case m.phase == phaseAsk:
		content = m.askView()
	case m.phase == phaseSessions:
		content = m.sessionsView()
	case m.phase == phaseEditor:
		content = m.editorView()
	case m.phase == phaseMemoryEditor:
		content = m.memoryEditorView()
	default:
		content = m.chatView()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = colorBG
	return v
}

func (m *Model) loginView() string {
	title := m.styles.Header.Render(" Harlequin ")
	prompt := "Email"
	switch m.phase {
	case phaseLoginPass, phaseRegisterPass:
		prompt = "Password"
	case phaseRegisterEmail:
		prompt = "New account email"
	case phaseRegisterCode:
		prompt = "Verification code"
	}
	var sb strings.Builder
	sb.WriteString(title + "\n\n")
	sb.WriteString(m.styles.Accent.Render("Sign in to "+m.cfg.ServerURL) + "\n\n")
	for _, b := range m.blocks {
		sb.WriteString(m.renderBlock(b) + "\n")
	}
	sb.WriteString("\n" + m.styles.Status.Render(prompt+":") + "\n")
	sb.WriteString(m.styles.InputBox.Render(m.input.View()))
	return sb.String()
}

func (m *Model) chatView() string {
	help := m.styles.Help.Render("enter: send · shift+enter/alt+enter: newline · tab: complete · ↑/↓: history · pgup/pgdn: scroll · /help · ctrl+c: quit")

	// Overlay the slash-command autocomplete menu over the bottom of the
	// transcript so the input stays put and the layout height is unchanged.
	vpView := m.vp.View()
	if menu := m.renderSlashMenuLines(); len(menu) > 0 {
		vpView = overlayBottomLines(vpView, menu)
	}

	// In a project, show the chatroom as a right-hand side-pane next to the
	// transcript (the header/composer/help stay full width).
	mid := vpView
	if m.activeProjectID > 0 {
		mid = lipgloss.JoinHorizontal(lipgloss.Top, vpView, m.renderChatPane(m.vp.Height()))
	}

	var sb strings.Builder
	sb.WriteString(m.renderHeaderLine() + "\n")
	sb.WriteString(m.renderAlerts()) // persistent alert box (already newline-terminated)
	sb.WriteString(mid + "\n")
	sb.WriteString(m.styles.InputBox.Render(m.input.View()) + "\n")
	sb.WriteString(help)
	return sb.String()
}

// chatPaneWidth is the width of the project chatroom side-pane.
const chatPaneWidth = 34

// renderChatPane renders the project chatroom side-pane (recent messages).
func (m *Model) renderChatPane(height int) string {
	var b strings.Builder
	b.WriteString(m.styles.Accent.Render("💬 "+truncate(m.activeProjectName, chatPaneWidth-4)) + "\n")
	// Show the most recent messages that fit.
	msgs := m.chatMessages
	max := height - 2
	if max < 1 {
		max = 1
	}
	if len(msgs) > max {
		msgs = msgs[len(msgs)-max:]
	}
	for _, cm := range msgs {
		who := cm.Email
		if i := strings.IndexByte(who, '@'); i > 0 {
			who = who[:i]
		}
		b.WriteString(m.styles.Help.Render(truncate(who, chatPaneWidth-2)) + "\n")
		b.WriteString(truncate(cm.Content, chatPaneWidth-2) + "\n")
	}
	if len(m.chatMessages) == 0 {
		b.WriteString(m.styles.Help.Render("(no messages — /say hi)") + "\n")
	}
	return lipgloss.NewStyle().
		Width(chatPaneWidth).Height(height).
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(colorBorder).
		PaddingLeft(1).Render(b.String())
}
