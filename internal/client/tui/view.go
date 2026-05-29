package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
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
	// header(1) + status(1) + input box(inputHeight+2 border) + help(1)
	chrome := 1 + 1 + inputHeight + 2 + 1
	vpHeight := m.height - chrome
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.vp.SetWidth(m.width)
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
	case m.phase == phaseLoginUser || m.phase == phaseLoginPass:
		content = m.loginView()
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
	prompt := "Username"
	if m.phase == phaseLoginPass {
		prompt = "Password"
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
	header := m.styles.Header.Render(" Harlequin ")
	status := m.statusMsg
	if m.loading {
		status = m.spin.View() + " thinking…  (Esc to cancel)"
	}
	statusLine := m.styles.Status.Render(status)

	help := m.styles.Help.Render("enter: send · shift+enter: newline · ↑/↓: history · /help · ctrl+c: quit")

	var sb strings.Builder
	sb.WriteString(header + "  " + statusLine + "\n")
	sb.WriteString(m.vp.View() + "\n")
	sb.WriteString(m.styles.InputBox.Render(m.input.View()) + "\n")
	sb.WriteString(help)
	return sb.String()
}
