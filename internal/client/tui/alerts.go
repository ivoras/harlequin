package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// Alerts are server notifications shown in a persistent box above the transcript.
// They are not part of the session/conversation: they sit in their own region and
// stay visible until dismissed (/dismiss). In the future the same alerts can be
// delivered to other interfaces (e.g. Telegram).

// addAlert adds a notification to the alert box (deduping by id) and re-lays out so
// the box's height is reserved above the viewport.
func (m *Model) addAlert(n types.Notification) {
	for _, a := range m.alerts {
		if a.ID == n.ID {
			return
		}
	}
	m.alerts = append(m.alerts, n)
	m.layout()
	m.refreshViewport()
}

// alertLineCount is how many terminal rows the alert box occupies (0 when empty),
// so layout can subtract them from the viewport height.
func (m *Model) alertLineCount() int {
	if len(m.alerts) == 0 {
		return 0
	}
	return len(m.alerts) + 1 // one row per alert + a blank separator
}

// renderAlerts renders the alert box (empty string when there are no alerts).
func (m *Model) renderAlerts() string {
	if len(m.alerts) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, n := range m.alerts {
		label := n.Title
		if n.Description != "" {
			label += " — " + n.Description
		}
		if strings.TrimSpace(n.Prompt) != "" {
			label += fmt.Sprintf("  · /run %d", i+1)
		}
		line := fmt.Sprintf("🔔 [%d] %s · /dismiss %d", i+1, label, i+1)
		width := m.width - 2
		if width < 8 {
			width = 8
		}
		sb.WriteString(m.styles.Alert.Render(truncate(line, width)))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

// dismissAlert handles "/dismiss [n|all]": ack the alert(s) server-side and remove
// them from the box. With no argument it dismisses all.
func (m *Model) dismissAlert(args []string) tea.Cmd {
	if len(m.alerts) == 0 {
		return infoCmd("no alerts to dismiss")
	}
	if len(args) == 0 || args[0] == "all" {
		ids := make([]int64, len(m.alerts))
		for i, a := range m.alerts {
			ids[i] = a.ID
		}
		m.alerts = nil
		m.layout()
		m.refreshViewport()
		return m.ackAlertsCmd(ids)
	}
	idx, err := strconv.Atoi(args[0])
	if err != nil || idx < 1 || idx > len(m.alerts) {
		return infoCmd("usage: /dismiss [n|all]")
	}
	id := m.alerts[idx-1].ID
	m.alerts = append(m.alerts[:idx-1], m.alerts[idx:]...)
	m.layout()
	m.refreshViewport()
	return m.ackAlertsCmd([]int64{id})
}

// runAlert handles "/run <n>": send the alert's prompt as a message, then dismiss it.
func (m *Model) runAlert(args []string) tea.Cmd {
	if len(args) != 1 {
		return infoCmd("usage: /run <n>")
	}
	idx, err := strconv.Atoi(args[0])
	if err != nil || idx < 1 || idx > len(m.alerts) {
		return infoCmd("no such alert")
	}
	a := m.alerts[idx-1]
	if strings.TrimSpace(a.Prompt) == "" {
		return infoCmd("that alert has no prompt to run")
	}
	m.alerts = append(m.alerts[:idx-1], m.alerts[idx:]...)
	m.layout()
	return tea.Batch(m.ackAlertsCmd([]int64{a.ID}), m.sendMessage(a.Prompt))
}

// ackAlertsCmd marks the given notifications handled (best-effort).
func (m *Model) ackAlertsCmd(ids []int64) tea.Cmd {
	return func() tea.Msg {
		for _, id := range ids {
			_ = m.client.AckNotification(context.Background(), id)
		}
		return nil
	}
}
