package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	bkey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// notificationsInterval is how often the client polls the server for pending
// notifications after the initial post-login check.
const notificationsInterval = time.Minute

type notificationsTickMsg struct{}
type notificationsMsg struct{ list []types.Notification }

// notifyTick re-arms the once-a-minute notification poll.
func notifyTick() tea.Cmd {
	return tea.Tick(notificationsInterval, func(time.Time) tea.Msg { return notificationsTickMsg{} })
}

// fetchNotificationsCmd loads pending notifications in the background.
func (m *Model) fetchNotificationsCmd() tea.Cmd {
	return func() tea.Msg {
		list, err := m.client.ListNotifications(context.Background())
		if err != nil {
			return notificationsMsg{} // ignore transient poll errors
		}
		return notificationsMsg{list: list}
	}
}

// ackNotifyCmd marks a notification handled (best-effort).
func (m *Model) ackNotifyCmd(id int64) tea.Cmd {
	return func() tea.Msg {
		_ = m.client.AckNotification(context.Background(), id)
		return nil
	}
}

// handleNotifications renders pending notifications and auto-runs at most one
// prompt per pass. It defers entirely while a turn is streaming so an auto-run
// can't collide with an in-flight conversation; deferred ones stay pending and
// are retried on the next tick.
func (m *Model) handleNotifications(list []types.Notification) tea.Cmd {
	if m.phase != phaseChat || m.loading || len(list) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	ranOne := false
	for _, n := range list {
		// Control notifications: a session-title update refreshes the header for the
		// matching conversation; it is acked but never shown as a chat message.
		if n.Kind == types.NotifyKindSessionTitle {
			if n.ConversationID != nil && *n.ConversationID == m.conversationID {
				m.convTitle = n.Title
			}
			cmds = append(cmds, m.ackNotifyCmd(n.ID))
			continue
		}
		autoRun := n.AutoRun && strings.TrimSpace(n.Prompt) != ""
		if autoRun && ranOne {
			continue // run one prompt at a time; pick up the rest next tick
		}
		m.appendBlock("notification", renderNotification(n))
		cmds = append(cmds, m.ackNotifyCmd(n.ID))
		if autoRun {
			cmds = append(cmds, m.startTurn(n.Prompt))
			ranOne = true
		}
	}
	return tea.Batch(cmds...)
}

// renderNotification formats a notification for the transcript.
func renderNotification(n types.Notification) string {
	if n.Description != "" {
		return "🔔 " + n.Title + " — " + n.Description
	}
	return "🔔 " + n.Title
}

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
		// Re-render so the in-transcript thinking indicator animates (spinner step
		// and colour glow) while a turn is in flight.
		if m.loading {
			m.refreshViewport()
		}
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
		m.input.Placeholder = loginPrompt
		return m, m.input.Focus()

	case chatReadyMsg:
		m.phase = phaseChat
		m.user = msg.user
		m.conversationID = msg.conversationID
		m.convTitle = ""
		m.input.Placeholder = "Type a message, or /help for commands"
		m.blocks = nil
		m.appendConnectedStatus()
		m.layout()
		// Check for server notifications now, then once a minute.
		return m, tea.Batch(m.input.Focus(), m.fetchNotificationsCmd(), notifyTick())

	case notificationsTickMsg:
		return m, tea.Batch(m.fetchNotificationsCmd(), notifyTick())

	case notificationsMsg:
		return m, m.handleNotifications(msg.list)

	case loginDoneMsg:
		if msg.err != nil {
			m.appendBlock("error", msg.err.Error())
			m.phase = phaseLoginUser
			m.input.Reset()
			m.input.Placeholder = loginPrompt
			return m, m.input.Focus()
		}
		// Persist token.
		m.cfg.Token = m.client.Token()
		_ = m.cfg.Save()
		return m, m.bootstrapChat()

	case registerSentMsg:
		if msg.err != nil {
			m.appendBlock("error", msg.err.Error())
			m.phase = phaseLoginUser
			m.input.Reset()
			m.input.Placeholder = loginPrompt
			return m, m.input.Focus()
		}
		m.appendBlock("info", "We sent a verification code to "+msg.email+". Enter it below (check the server console if email isn't configured).")
		m.phase = phaseRegisterCode
		m.input.Reset()
		m.input.Placeholder = "Enter verification code"
		return m, m.input.Focus()

	case verifyDoneMsg:
		if msg.err != nil {
			// Stay on the code step so the user can retry.
			m.appendBlock("error", msg.err.Error())
			m.input.Reset()
			m.input.Placeholder = "Enter verification code"
			return m, m.input.Focus()
		}
		// Verified + auto-logged-in: persist the token and enter chat.
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
		m.ppProgress = ""
		m.flushStreaming()
		if m.pendingTiming != nil {
			m.appendBlock("status", formatTiming(m.pendingTiming))
			m.pendingTiming = nil
		}
		if msg.err != nil && msg.err != context.Canceled {
			m.appendBlock("error", msg.err.Error())
		}
		m.refreshViewport()
		if len(m.pendingAsk) > 0 {
			return m, m.enterAsk()
		}
		// Drain the next queued message, if any.
		if len(m.msgQueue) > 0 {
			next := m.msgQueue[0]
			m.msgQueue = m.msgQueue[1:]
			return m, m.sendMessage(next)
		}
		return m, nil

	case askPulseMsg:
		if m.phase == phaseAsk {
			m.askFrame++
			return m, askPulseTick()
		}
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
	case types.SSEPromptProgress:
		if ev.PromptTotal > 0 {
			pct := ev.PromptProcessed * 100 / ev.PromptTotal
			label := "Processing prompt"
			if ev.Source != "" {
				label = ev.Source + ": processing prompt"
			}
			m.ppProgress = fmt.Sprintf("%s %d%% (%d/%d tok)", label, pct, ev.PromptProcessed, ev.PromptTotal)
			m.refreshViewport()
		}
	case types.SSEThinking:
		m.ppProgress = "" // prefill done once tokens flow
		if m.cfg.ShowThinking {
			m.streamingThinking.WriteString(ev.Thinking)
			m.refreshViewport()
		}
	case types.SSEToken:
		m.ppProgress = ""
		m.streaming.WriteString(ev.Text)
		m.refreshViewport()
	case types.SSEToolCall:
		// Commit the reasoning/text that led to this call first, so the tool
		// call appears after them in chronological order.
		m.flushStreaming()
		m.appendBlock("tool", "⚙ "+ev.ToolName+"("+truncate(ev.ToolArgs, 120)+")")
	case types.SSEToolResult:
		m.ppProgress = "" // clear any delegated (e.g. WebFetch) prefill progress
		m.appendBlock("tool", "  ↳ "+truncate(strings.TrimSpace(ev.Output), 200))
	case types.SSEError:
		m.appendBlock("error", ev.Error)
	case types.SSEAskUser:
		// Flush any partial reasoning/text first, then collect the question; it is
		// presented interactively when the turn ends (handles multiple questions).
		m.flushStreaming()
		m.pendingAsk = append(m.pendingAsk, askItem{question: strings.TrimSpace(ev.Text), options: ev.Options})
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
	case phaseAsk:
		return m.handleAskKey(msg, key)
	case phaseLoginUser:
		if key == "enter" {
			v := strings.TrimSpace(m.input.Value())
			m.input.Reset()
			// "register" at the email prompt switches to the sign-up flow.
			if strings.EqualFold(v, "register") {
				m.appendBlock("info", "Create an account — enter your email.")
				m.input.Placeholder = "Enter email"
				m.phase = phaseRegisterEmail
				return m, nil
			}
			m.loginUser = v
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
	case phaseRegisterEmail:
		if key == "enter" {
			m.regEmail = strings.TrimSpace(m.input.Value())
			m.input.Reset()
			m.input.Placeholder = "Choose a password (min 8 chars)"
			m.phase = phaseRegisterPass
			return m, nil
		}
	case phaseRegisterPass:
		if key == "enter" {
			m.regPass = m.input.Value()
			m.input.Reset()
			m.appendBlock("info", "registering "+m.regEmail+"…")
			return m, m.doRegister(m.regEmail, m.regPass)
		}
	case phaseRegisterCode:
		if key == "enter" {
			code := strings.TrimSpace(m.input.Value())
			m.input.Reset()
			m.appendBlock("info", "verifying…")
			return m, m.doVerify(m.regEmail, code)
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
			// A turn is already running: queue the message instead of starting a
			// second concurrent stream. It's sent when the current turn finishes.
			if m.loading {
				m.msgQueue = append(m.msgQueue, text)
				m.refreshViewport()
				return m, nil
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

// doRegister starts self-registration; the server emails a verification code.
func (m *Model) doRegister(email, pass string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.Register(context.Background(), email, pass)
		return registerSentMsg{email: email, err: err}
	}
}

// doVerify submits the emailed code; on success the client holds a fresh token.
func (m *Model) doVerify(email, code string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.Verify(context.Background(), email, code)
		return verifyDoneMsg{err: err}
	}
}

// sendMessage posts the message and starts streaming via the program.
func (m *Model) sendMessage(text string) tea.Cmd { return m.sendMessageAs(text, text) }

// sendMessageAs shows display in the transcript but sends sendText to the
// server (used by the ask flow to show concise answers but send full Q&A).
func (m *Model) sendMessageAs(display, sendText string) tea.Cmd {
	m.appendBlock("user", display)
	return m.startTurn(sendText)
}

// startTurn sends text to the agent and streams the reply, without adding a user
// block to the transcript (callers render their own context, e.g. a notification).
func (m *Model) startTurn(text string) tea.Cmd {
	m.loading = true
	m.turnStart = time.Now()
	m.refreshViewport() // show the thinking indicator immediately, before the first tick
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
