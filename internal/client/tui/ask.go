package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// askPulseMsg drives the selected-row marker animation while answering questions.
type askPulseMsg struct{}

func askPulseTick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return askPulseMsg{} })
}

// askMarkerFrames animate the selected option's marker (a gentle blink).
var askMarkerFrames = []string{"▶", "▷"}

const askOtherLabel = "✎ Other — type your own answer"

// enterAsk switches into the interactive answering modal and starts the
// animation. Call when a turn ends with collected ask_user questions.
func (m *Model) enterAsk() tea.Cmd {
	m.phase = phaseAsk
	m.askIndex, m.askSel, m.askFrame = 0, 0, 0
	m.askAnswers = make([]string, 0, len(m.pendingAsk))
	m.askOther = false
	return askPulseTick()
}

// handleAskKey processes keys while answering questions.
func (m *Model) handleAskKey(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	if m.askIndex >= len(m.pendingAsk) {
		return m, nil
	}
	cur := m.pendingAsk[m.askIndex]
	total := len(cur.options) + 1 // +1 for the "Other" entry

	if m.askOther {
		switch key {
		case "esc":
			m.askOther = false
			m.input.Reset()
			return m, nil
		case "enter":
			if !msg.Key().Mod.Contains(tea.ModShift) {
				ans := strings.TrimSpace(m.input.Value())
				if ans == "" {
					return m, nil
				}
				m.input.Reset()
				m.askOther = false
				return m.recordAnswer(ans)
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	switch key {
	case "esc":
		return m.cancelAsk()
	case "up":
		m.askSel = (m.askSel - 1 + total) % total
		return m, nil
	case "down":
		m.askSel = (m.askSel + 1) % total
		return m, nil
	case "enter":
		if m.askSel >= len(cur.options) { // the "Other" entry → free text
			m.askOther = true
			m.input.Reset()
			m.input.Placeholder = "Type your answer, then Enter"
			return m, m.input.Focus()
		}
		return m.recordAnswer(cur.options[m.askSel])
	}
	return m, nil
}

// recordAnswer stores the answer for the current question and advances, or
// finalizes when every question is answered.
func (m *Model) recordAnswer(ans string) (tea.Model, tea.Cmd) {
	m.askAnswers = append(m.askAnswers, ans)
	m.askIndex++
	m.askSel = 0
	if m.askIndex >= len(m.pendingAsk) {
		return m.finalizeAsk()
	}
	return m, nil
}

// finalizeAsk records the Q&A in the transcript and sends the answers as the
// next user message.
func (m *Model) finalizeAsk() (tea.Model, tea.Cmd) {
	items := m.pendingAsk
	answers := m.askAnswers

	m.pendingAsk = nil
	m.askAnswers = nil
	m.askIndex, m.askSel, m.askFrame = 0, 0, 0
	m.askOther = false
	m.phase = phaseChat
	m.input.Reset()
	m.input.Placeholder = "Type a message, or /help for commands"

	m.appendBlock("assistant", renderAskQuestions(items))
	display, send := buildAskAnswer(items, answers)
	return m, tea.Batch(m.input.Focus(), m.sendMessageAs(display, send))
}

// cancelAsk dismisses the questions without answering (the user can type freely).
func (m *Model) cancelAsk() (tea.Model, tea.Cmd) {
	m.pendingAsk = nil
	m.askAnswers = nil
	m.askOther = false
	m.askIndex, m.askSel, m.askFrame = 0, 0, 0
	m.phase = phaseChat
	m.input.Reset()
	m.input.Placeholder = "Type a message, or /help for commands"
	m.appendBlock("status", "questions dismissed — type a reply instead")
	m.refreshViewport()
	return m, m.input.Focus()
}

// askView renders the interactive answering modal.
func (m *Model) askView() string {
	var sb strings.Builder
	sb.WriteString(m.renderHeaderLine() + "\n\n")

	multi := len(m.pendingAsk) > 1
	if multi {
		sb.WriteString(m.styles.Error.Render(fmt.Sprintf("⚠ The assistant asked %d questions — answer them one at a time:", len(m.pendingAsk))))
		sb.WriteString("\n\n")
		for i, q := range m.pendingAsk {
			marker, style := "  ", m.styles.Help
			line := fmt.Sprintf("%d. %s", i+1, truncate(q.question, m.contentWidth()-12))
			switch {
			case i < m.askIndex:
				marker = m.styles.Accent.Render("✓")
				line += "  → " + m.askAnswers[i]
			case i == m.askIndex:
				marker = m.styles.Accent.Render("▶")
				style = m.styles.Assistant
			}
			sb.WriteString(" " + marker + " " + style.Render(line) + "\n")
		}
		sb.WriteString("\n")
	}

	cur := m.pendingAsk[m.askIndex]
	if multi {
		sb.WriteString(m.styles.Status.Render(fmt.Sprintf("Question %d of %d", m.askIndex+1, len(m.pendingAsk))) + "\n")
	}
	sb.WriteString(m.styles.Assistant.Bold(true).Render(wrapWidth(m.contentWidth(), cur.question)) + "\n\n")

	marker := askMarkerFrames[m.askFrame%len(askMarkerFrames)]
	rows := append(append([]string{}, cur.options...), askOtherLabel)
	for i, opt := range rows {
		text := truncate(opt, m.contentWidth()-4)
		if i == m.askSel {
			sel := m.styles.Accent.Render(marker+" ") + m.styles.Selected.Render(" "+text+" ")
			sb.WriteString(sel + "\n")
		} else {
			sb.WriteString("  " + m.styles.Help.Render(text) + "\n")
		}
	}

	sb.WriteString("\n")
	if m.askOther {
		sb.WriteString(m.styles.InputBox.Render(m.input.View()) + "\n")
		sb.WriteString(m.styles.Help.Render("enter: submit · esc: back to options"))
	} else {
		sb.WriteString(m.styles.Help.Render("↑/↓: move · enter: select · esc: dismiss"))
	}
	return sb.String()
}

// renderAskQuestions renders the question(s) for the transcript record.
func renderAskQuestions(items []askItem) string {
	if len(items) == 1 {
		return strings.TrimSpace(items[0].question)
	}
	var sb strings.Builder
	sb.WriteString("Asked:")
	for i, q := range items {
		fmt.Fprintf(&sb, "\n  %d. %s", i+1, strings.TrimSpace(q.question))
	}
	return sb.String()
}

// buildAskAnswer returns (display, send): a concise transcript line and the
// detailed message sent to the model (questions mapped to answers).
func buildAskAnswer(items []askItem, answers []string) (string, string) {
	if len(items) == 1 {
		return answers[0], answers[0]
	}
	var disp, send strings.Builder
	send.WriteString("Here are my answers:")
	for i := range items {
		fmt.Fprintf(&disp, "%d. %s\n", i+1, answers[i])
		fmt.Fprintf(&send, "\n%d. %s\n   → %s", i+1, strings.TrimSpace(items[i].question), answers[i])
	}
	return strings.TrimRight(disp.String(), "\n"), send.String()
}
