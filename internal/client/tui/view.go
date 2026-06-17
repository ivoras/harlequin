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
	// header(1) + input box(inputHeight+2 border) + help(1)
	chrome := 1 + inputHeight + 2 + 1
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
	case m.phase == phaseLoginUser || m.phase == phaseLoginPass ||
		m.phase == phaseRegisterEmail || m.phase == phaseRegisterPass || m.phase == phaseRegisterCode:
		content = m.loginView()
	case m.phase == phaseAsk:
		content = m.askView()
	case m.phase == phaseSessions:
		content = m.sessionsView()
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

	var sb strings.Builder
	sb.WriteString(m.renderHeaderLine() + "\n")
	sb.WriteString(vpView + "\n")
	sb.WriteString(m.styles.InputBox.Render(m.input.View()) + "\n")
	sb.WriteString(help)
	return sb.String()
}
