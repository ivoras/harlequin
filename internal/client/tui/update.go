package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	bkey "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/ivoras/harlequin/internal/client/apiclient"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// ackNotifyCmd marks a notification handled (best-effort).
func (m *Model) ackNotifyCmd(id int64) tea.Cmd {
	return func() tea.Msg {
		_ = m.client.AckNotification(context.Background(), id)
		return nil
	}
}

// handlePushedNotification handles one server-pushed notification (notifications
// are delivered over the session WebSocket, not polled). A session-title update
// refreshes the header; other notifications are shown and acked. An auto-run
// prompt that arrives mid-turn is queued and drained when the turn ends, so it
// can't collide with an in-flight turn.
func (m *Model) handlePushedNotification(n types.Notification) tea.Cmd {
	if n.Kind == types.NotifyKindSessionTitle {
		if n.SessionID != nil && *n.SessionID == m.sessionID {
			m.sessTitle = n.Title
			m.refreshViewport()
		}
		return m.ackNotifyCmd(n.ID)
	}
	autoRun := n.AutoRun && strings.TrimSpace(n.Prompt) != ""
	if autoRun {
		// Auto-run prompts execute (deferred if a turn is in flight); they are a
		// behaviour, not a passive alert, so they don't enter the alert box.
		if m.loading || m.phase != phaseChat {
			m.pendingNotifs = append(m.pendingNotifs, n)
			return nil
		}
		return tea.Batch(m.ackNotifyCmd(n.ID), m.startTurn(n.Prompt))
	}
	// Passive notification: show it in the persistent alert box (not the
	// transcript), kept until the user dismisses it.
	m.addAlert(n)
	return nil
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
		m.switchSession(msg.sessionID)
		m.sessTitle = ""
		m.input.Placeholder = "Type a message, or /help for commands"
		m.blocks = nil
		m.appendConnectedStatus()
		m.layout()
		cmds := []tea.Cmd{m.input.Focus()}
		// Connect the session socket now so the server can push notifications and
		// stream any in-flight turn. On resume, load committed history first (which
		// then opens the socket); otherwise open it directly.
		if msg.resume {
			cmds = append(cmds, m.loadHistoryCmd(msg.sessionID))
		} else {
			cmds = append(cmds, m.openSessionCmd())
		}
		return m, tea.Batch(cmds...)

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

	case skillEditorLoadedMsg:
		if msg.err != nil {
			m.appendBlock("error", msg.err.Error())
			return m, nil
		}
		return m, m.startSkillEditor(msg)

	case skillEditorSavedMsg:
		if msg.err != nil {
			if m.editor != nil {
				m.editor.status = "save failed: " + msg.err.Error()
			}
			return m, nil
		}
		m.phase = phaseChat
		m.editor = nil
		m.appendBlock("info", "saved "+msg.name+"/"+msg.relpath)
		return m, m.input.Focus()

	case streamEventMsg:
		return m.handleStreamEvent(msg.ev)

	case sessionOpenedMsg:
		if msg.err != nil {
			m.loading = false
			m.appendBlock("error", "connect: "+msg.err.Error())
			m.refreshViewport()
			return m, nil
		}
		m.session = msg.s
		s := msg.s
		// Pump events into the program until the socket ends.
		go func() {
			for ev := range s.Events() {
				if m.prog != nil {
					m.prog.Send(streamEventMsg{ev})
				}
			}
			if m.prog != nil {
				m.prog.Send(streamSocketClosedMsg{})
			}
		}()
		// Flush any prompts queued while (re)connecting.
		for _, p := range m.pendingSubmit {
			_ = m.session.Submit(p)
		}
		m.pendingSubmit = nil
		return m, nil

	case streamSocketClosedMsg:
		m.session = nil
		// The server keeps running the session; if a turn is in flight, reconnect
		// and resume from the last seen seq.
		if m.loading && m.sessionID != 0 {
			return m, m.openSessionCmd()
		}
		return m, nil

	case projectSwitchedMsg:
		if msg.err != nil {
			m.appendBlock("error", msg.err.Error())
			m.refreshViewport()
			return m, nil
		}
		m.activeProjectID = msg.id
		m.activeProjectName = msg.name
		m.switchSession(msg.sessionID) // resets the socket; keeps activeProjectID
		m.phase = phaseChat
		m.blocks = nil
		m.chatMessages = nil
		m.appendBlock("status", "switched to project "+msg.name+" — /say <msg> to chat, /project leave to exit")
		m.layout()
		m.refreshViewport()
		return m, tea.Batch(m.input.Focus(), m.loadHistoryCmd(msg.sessionID), m.openChatCmd(msg.id))

	case chatOpenedMsg:
		if msg.err != nil {
			m.appendBlock("error", "chatroom: "+msg.err.Error())
			return m, nil
		}
		m.chat = msg.c
		c := msg.c
		go m.pumpChat(c)
		return m, nil

	case chatRecvMsg:
		for _, x := range m.chatMessages {
			if x.ID == msg.m.ID {
				return m, nil // dedupe (history + live overlap on reconnect)
			}
		}
		m.chatMessages = append(m.chatMessages, msg.m)
		m.refreshViewport()
		return m, nil

	case historyLoadedMsg:
		if msg.sessionID != m.sessionID {
			return m, nil // switched away while loading
		}
		if msg.err != nil {
			m.appendBlock("error", msg.err.Error())
			m.refreshViewport()
			return m, nil
		}
		// Hold the history; the synced frame decides where the in-flight turn (if
		// any) begins. Open the socket to receive it.
		m.coldHistory = msg.msgs
		return m, m.openSessionCmd()

	case sessionsLoadedMsg:
		if msg.err != nil {
			m.appendBlock("error", msg.err.Error())
			m.refreshViewport()
			return m, nil
		}
		if len(msg.list) == 0 {
			m.appendBlock("info", "no sessions to resume")
			m.refreshViewport()
			return m, nil
		}
		m.sessionList = msg.list
		m.sessionSel = 0
		m.phase = phaseSessions
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
	if ev.Seq > 0 {
		m.lastSeq = ev.Seq // resume watermark
	}
	switch ev.Type {
	case types.SSESynced:
		// Resume handshake. When we hold committed history (a cold resume), render
		// it now — trimmed to before the in-flight turn when one is running — and the
		// replayed buffer reconstructs that turn. Warm reconnects carry no history.
		if m.coldHistory != nil {
			msgs := m.coldHistory
			if ev.Running {
				msgs = trimHistory(msgs, ev.CommittedThrough)
			}
			m.renderHistory(msgs)
			m.loading = ev.Running
			m.coldHistory = nil
			m.refreshViewport()
		}
	case types.SSEUserMessage:
		// The server echoes the prompt as the first event of a turn. Skip the echo
		// for prompts we already rendered locally; render it otherwise (cold resume
		// / a prompt submitted from another client).
		if m.optimisticUser > 0 {
			m.optimisticUser--
			break
		}
		m.flushStreaming()
		m.appendBlock("user", ev.Text)
		m.loading = true
		m.refreshViewport()
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
	case types.SSENotification:
		if ev.Notification != nil {
			return m, m.handlePushedNotification(*ev.Notification)
		}
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
		// The turn is complete (the socket stays open for the next one).
		return m, m.finalizeTurn()
	}
	return m, nil
}

// finalizeTurn ends the current turn: commit streamed text, show timing, then
// either enter the ask flow or drain the next queued message.
func (m *Model) finalizeTurn() tea.Cmd {
	m.loading = false
	m.ppProgress = ""
	m.flushStreaming()
	if m.pendingTiming != nil {
		m.appendBlock("status", formatTiming(m.pendingTiming))
		m.pendingTiming = nil
	}
	m.refreshViewport()
	if len(m.pendingAsk) > 0 {
		return m.enterAsk()
	}
	if len(m.msgQueue) > 0 {
		next := m.msgQueue[0]
		m.msgQueue = m.msgQueue[1:]
		return m.sendMessage(next)
	}
	// Drain one auto-run notification that arrived during the turn.
	if len(m.pendingNotifs) > 0 {
		n := m.pendingNotifs[0]
		m.pendingNotifs = m.pendingNotifs[1:]
		return m.handlePushedNotification(n)
	}
	return nil
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

	// Interrupt the in-flight turn (the session stays alive on the server).
	if key == "esc" {
		if m.loading && m.session != nil {
			_ = m.session.Interrupt()
			m.loading = false
			return m, nil
		}
	}

	switch m.phase {
	case phaseAsk:
		return m.handleAskKey(msg, key)
	case phaseSessions:
		return m.handleSessionsKey(key)
	case phaseEditor:
		return m.handleEditorKey(msg, key)
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
// server (used by the ask flow to show concise answers but send full Q&A). Since
// we render the prompt locally here, we skip the server's echo of it.
func (m *Model) sendMessageAs(display, sendText string) tea.Cmd {
	m.appendBlock("user", display)
	m.optimisticUser++
	return m.startTurn(sendText)
}

// startTurn submits text to the live session over the WebSocket, opening (or
// reopening) the socket first if needed. It does not render a user block — callers
// that want one render it themselves (and bump optimisticUser); a prompt with no
// local block is echoed back and rendered from the stream.
func (m *Model) startTurn(text string) tea.Cmd {
	m.loading = true
	m.turnStart = time.Now()
	m.refreshViewport() // show the thinking indicator immediately, before the first tick

	if m.session == nil {
		m.pendingSubmit = append(m.pendingSubmit, text)
		return tea.Batch(thinkPulseTick(), m.openSessionCmd())
	}
	if err := m.session.Submit(text); err != nil {
		// Socket died between turns: reopen and resume, then send.
		m.session = nil
		m.pendingSubmit = append(m.pendingSubmit, text)
		return tea.Batch(thinkPulseTick(), m.openSessionCmd())
	}
	return thinkPulseTick()
}

// switchSession points the client at session id, tearing down any open socket and
// resetting resume bookkeeping (the next turn lazily opens a fresh socket).
func (m *Model) switchSession(id int64) {
	if m.session != nil {
		_ = m.session.Close()
		m.session = nil
	}
	m.sessionID = id
	m.lastSeq = 0
	m.optimisticUser = 0
	m.pendingSubmit = nil
	m.loading = false
}

// openSessionCmd (re)opens the session WebSocket, resuming from the last seen seq.
func (m *Model) openSessionCmd() tea.Cmd {
	sessionID := m.sessionID
	haveSeq := m.lastSeq
	projectID := m.activeProjectID
	return func() tea.Msg {
		var s *apiclient.Session
		var err error
		if projectID > 0 {
			s, err = m.client.OpenProjectSession(context.Background(), projectID, sessionID, haveSeq)
		} else {
			s, err = m.client.OpenSession(context.Background(), sessionID, haveSeq)
		}
		return sessionOpenedMsg{s: s, err: err}
	}
}

// resumeSession switches to session id, loads its committed history, and
// reconnects to its live server-side goroutine (so any in-flight turn streams in).
func (m *Model) resumeSession(id int64) tea.Cmd {
	m.switchSession(id)
	m.phase = phaseChat
	m.blocks = nil
	m.appendBlock("status", fmt.Sprintf("resuming session #%d…", id))
	m.refreshViewport()
	return m.loadHistoryCmd(id)
}

// loadHistoryCmd fetches a session's committed messages for a resume (from the
// project corpus when in a project).
func (m *Model) loadHistoryCmd(id int64) tea.Cmd {
	projectID := m.activeProjectID
	return func() tea.Msg {
		var msgs []types.Message
		var err error
		if projectID > 0 {
			msgs, err = m.client.ProjectMessages(context.Background(), projectID, id)
		} else {
			msgs, err = m.client.Messages(context.Background(), id)
		}
		return historyLoadedMsg{sessionID: id, msgs: msgs, err: err}
	}
}

// resumeListCmd loads the recent sessions for the interactive picker.
func (m *Model) resumeListCmd(query string) tea.Cmd {
	return func() tea.Msg {
		list, err := m.client.ListSessions(context.Background(), query)
		return sessionsLoadedMsg{list: list, err: err}
	}
}

// renderHistory appends committed messages to the transcript as blocks.
func (m *Model) renderHistory(msgs []types.Message) {
	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			m.appendBlock("user", msg.Content)
		case "assistant":
			if strings.TrimSpace(msg.Content) != "" {
				m.appendBlock("assistant", msg.Content)
			}
		case "tool":
			m.appendBlock("tool", "  ↳ "+truncate(strings.TrimSpace(msg.Content), 200))
		}
	}
}

// trimHistory drops messages belonging to the in-flight turn (id > through); the
// replayed event buffer reconstructs that turn instead.
func trimHistory(msgs []types.Message, through int64) []types.Message {
	out := msgs[:0:0]
	for _, m := range msgs {
		if m.ID <= through {
			out = append(out, m)
		}
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// collapseWS collapses every run of whitespace (incl. newlines from extracted
// PDFs) into a single space so a chunk reads as one clean passage.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
