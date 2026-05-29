package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.refreshViewport()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case loginNeededMsg:
		m.phase = phaseLoginUser
		m.input.Reset()
		m.input.Placeholder = "Enter username"
		return m, m.input.Focus()

	case chatReadyMsg:
		m.phase = phaseChat
		m.user = msg.user
		m.conversationID = msg.conversationID
		m.input.Placeholder = "Type a message, or /help for commands"
		m.statusMsg = "connected as " + msg.user.Username
		m.layout()
		return m, m.input.Focus()

	case loginDoneMsg:
		if msg.err != nil {
			m.appendBlock("error", msg.err.Error())
			m.phase = phaseLoginUser
			m.input.Reset()
			m.input.Placeholder = "Enter username"
			return m, m.input.Focus()
		}
		// Persist token.
		m.cfg.Token = m.client.Token()
		_ = m.cfg.Save()
		return m, m.bootstrapChat()

	case errMsg:
		m.loading = false
		m.appendBlock("error", msg.err.Error())
		return m, nil

	case infoMsg:
		m.loading = false
		m.appendBlock("info", msg.text)
		return m, nil

	case streamEventMsg:
		return m.handleStreamEvent(msg.ev)

	case streamEndMsg:
		m.loading = false
		if m.cfg.ShowThinking && m.streamingThinking.Len() > 0 {
			m.appendBlock("thinking", m.streamingThinking.String())
			m.streamingThinking.Reset()
		}
		if m.streaming.Len() > 0 {
			m.appendBlock("assistant", m.streaming.String())
			m.streaming.Reset()
		}
		if msg.err != nil && msg.err != context.Canceled {
			m.appendBlock("error", msg.err.Error())
		}
		m.refreshViewport()
		return m, nil
	}

	// Forward to focused component.
	var cmd tea.Cmd
	if m.phase == phaseChat {
		m.vp, cmd = m.vp.Update(msg)
		cmds = append(cmds, cmd)
	}
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *Model) handleStreamEvent(ev types.StreamEvent) (tea.Model, tea.Cmd) {
	switch ev.Type {
	case types.SSEThinking:
		if m.cfg.ShowThinking {
			m.streamingThinking.WriteString(ev.Thinking)
			m.refreshViewport()
		}
	case types.SSEToken:
		m.streaming.WriteString(ev.Text)
		m.refreshViewport()
	case types.SSEToolCall:
		m.appendBlock("tool", "⚙ "+ev.ToolName+"("+truncate(ev.ToolArgs, 120)+")")
	case types.SSEToolResult:
		m.appendBlock("tool", "  ↳ "+truncate(strings.TrimSpace(ev.Output), 200))
	case types.SSEError:
		m.appendBlock("error", ev.Error)
	case types.SSEDone:
		// handled by streamEndMsg
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Global quit.
	if key == "ctrl+c" {
		return m, tea.Quit
	}

	// Cancel in-flight stream.
	if key == "esc" {
		if m.loading && m.cancelStream != nil {
			m.cancelStream()
			m.loading = false
			return m, nil
		}
	}

	switch m.phase {
	case phaseLoginUser:
		if key == "enter" {
			m.loginUser = strings.TrimSpace(m.input.Value())
			m.input.Reset()
			m.input.Placeholder = "Enter password"
			// Note: password is shown; for simplicity we do not mask in v1.
			m.phase = phaseLoginPass
			return m, nil
		}
	case phaseLoginPass:
		if key == "enter" {
			pass := m.input.Value()
			m.input.Reset()
			user := m.loginUser
			m.appendBlock("info", "logging in as "+user+"…")
			return m, m.doLogin(user, pass)
		}
	case phaseChat:
		if key == "enter" && !msg.Key().Mod.Contains(tea.ModShift) {
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.Reset()
			if strings.HasPrefix(text, "/") {
				return m, m.handleSlash(text)
			}
			return m, m.sendMessage(text)
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) doLogin(user, pass string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.Login(context.Background(), user, pass)
		return loginDoneMsg{err: err}
	}
}

// sendMessage posts the message and starts streaming via the program.
func (m *Model) sendMessage(text string) tea.Cmd {
	m.appendBlock("user", text)
	m.loading = true
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelStream = cancel

	go func() {
		err := m.client.SendMessage(ctx, m.conversationID, text, func(ev types.StreamEvent) {
			if m.prog != nil {
				m.prog.Send(streamEventMsg{ev})
			}
		})
		if m.prog != nil {
			m.prog.Send(streamEndMsg{err: err})
		}
	}()
	return nil
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
