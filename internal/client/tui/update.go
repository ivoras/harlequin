package tui

import (
	"context"
	"fmt"
	"strings"

	bkey "charm.land/bubbles/v2/key"
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

	case thinkPulseMsg:
		if m.modelThinking() {
			return m, thinkPulseTick()
		}
		return m, nil

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
		m.blocks = nil
		m.appendConnectedStatus()
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
		m.flushStreaming()
		if m.pendingTiming != nil {
			m.appendBlock("status", formatTiming(m.pendingTiming))
			m.pendingTiming = nil
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
		// Commit the reasoning/text that led to this call first, so the tool
		// call appears after them in chronological order.
		m.flushStreaming()
		m.appendBlock("tool", "⚙ "+ev.ToolName+"("+truncate(ev.ToolArgs, 120)+")")
	case types.SSEToolResult:
		m.appendBlock("tool", "  ↳ "+truncate(strings.TrimSpace(ev.Output), 200))
	case types.SSEError:
		m.appendBlock("error", ev.Error)
	case types.SSEAskUser:
		// Flush any partial reasoning/text first so the question renders after it.
		m.flushStreaming()
		m.appendBlock("assistant", renderAskUser(ev.Text, ev.Options))
	case types.SSEDone:
		if ev.ContextMax > 0 {
			m.ctxMeter = contextMeterState{
				model: ev.Model,
				used:  ev.ContextTokens,
				max:   ev.ContextMax,
			}
		}
		m.pendingTiming = ev.Timing
	}
	return m, nil
}

// formatTiming renders a compact one-line model timing summary: prompt
// processing (PP) and token generation (TG) rates plus wall-clock time.
func formatTiming(t *types.TurnTiming) string {
	secs := func(ms int64) float64 { return float64(ms) / 1000 }
	pp := "—"
	if t.PPRate > 0 {
		pp = fmt.Sprintf("%.0f tok/s (%d tok / %.2fs)", t.PPRate, t.PromptTokens, secs(t.PrefillMS))
	}
	tg := "—"
	if t.TGRate > 0 {
		tg = fmt.Sprintf("%.1f tok/s (%d tok / %.2fs)", t.TGRate, t.CompletionTokens, secs(t.DecodeMS))
	}
	return fmt.Sprintf("⏱ PP %s · TG %s · %.2fs total", pp, tg, secs(t.TotalMS))
}

// renderAskUser formats an ask_user prompt: the question followed by any
// suggested options as a numbered list.
func renderAskUser(question string, options []string) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(question))
	for i, opt := range options {
		fmt.Fprintf(&sb, "\n  %d. %s", i+1, opt)
	}
	return sb.String()
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
		if bkey.Matches(msg, m.vp.KeyMap.PageUp) || bkey.Matches(msg, m.vp.KeyMap.PageDown) {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
		// Slash-command autocomplete menu (only when a "/" begins the line and
		// the command word is still being typed): navigate with ↑/↓, complete
		// with Tab, and Enter completes a partial or runs an exact command.
		if sugg := m.slashSuggestions(); len(sugg) > 0 {
			m.slashSel = clampSlashSel(m.slashSel, len(sugg))
			switch key {
			case "up":
				m.slashSel = (m.slashSel - 1 + len(sugg)) % len(sugg)
				return m, nil
			case "down":
				m.slashSel = (m.slashSel + 1) % len(sugg)
				return m, nil
			case "tab":
				m.completeSlash(sugg[m.slashSel])
				return m, nil
			case "enter":
				if !msg.Key().Mod.Contains(tea.ModShift) {
					v := strings.TrimSpace(m.input.Value())
					if isExactSlashCommand(v) {
						m.pushInputHistory(v)
						m.input.Reset()
						m.slashSel = 0
						return m, m.handleSlash(v)
					}
					m.completeSlash(sugg[m.slashSel])
					return m, nil
				}
			}
		}
		if key == "up" {
			if m.tryRecallHistory(-1) {
				return m, nil
			}
		}
		if key == "down" {
			if m.tryRecallHistory(1) {
				return m, nil
			}
		}
		if enterSends(msg) {
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.pushInputHistory(text)
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
	return thinkPulseTick()
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
