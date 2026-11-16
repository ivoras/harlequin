package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

const configUsage = `usage:
  /config                      list your saved config
  /config get <key>            show one key
  /config set <key> <value>    set a key (e.g. telegram.chat_id 123456789)
  /config rm <key>             delete a key`

func (m *Model) handleConfigSub(args []string) tea.Cmd {
	if len(args) == 0 || strings.ToLower(args[0]) == "list" {
		return func() tea.Msg {
			cfg, err := m.client.GetConfig(context.Background())
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{renderConfig(cfg)}
		}
	}
	switch strings.ToLower(args[0]) {
	case "get":
		if len(args) < 2 {
			return infoCmd("usage: /config get <key>")
		}
		key := args[1]
		return func() tea.Msg {
			cfg, err := m.client.GetConfig(context.Background())
			if err != nil {
				return errMsg{err}
			}
			v, ok := cfg[key]
			if !ok {
				return infoMsg{"(not set) " + key}
			}
			return infoMsg{key + " = " + v}
		}
	case "set":
		if len(args) < 3 {
			return infoCmd("usage: /config set <key> <value>")
		}
		key := args[1]
		value := strings.Join(args[2:], " ")
		return func() tea.Msg {
			if err := m.client.SetConfig(context.Background(), key, value); err != nil {
				return errMsg{err}
			}
			return infoMsg{"set " + key}
		}
	case "del":
		if len(args) < 2 {
			return infoCmd("usage: /config del <key>")
		}
		key := args[1]
		return func() tea.Msg {
			if err := m.client.DeleteConfig(context.Background(), key); err != nil {
				return errMsg{err}
			}
			return infoMsg{"removed " + key}
		}
	default:
		return infoCmd(configUsage)
	}
}

func (m *Model) handleTelegramSub(args []string) tea.Cmd {
	switch {
	case len(args) == 0 || args[0] == "connect":
		return func() tea.Msg {
			l, err := m.client.TelegramLink(context.Background())
			if err != nil {
				return errMsg{err}
			}
			return infoMsg{fmt.Sprintf("Open this link in Telegram and press Start (valid %d minutes, works once):\n  %s", l.ExpiresIn/60, l.URL)}
		}
	case args[0] == "disconnect":
		return func() tea.Msg {
			if err := m.client.TelegramUnlink(context.Background()); err != nil {
				return errMsg{err}
			}
			return infoMsg{"Telegram disconnected"}
		}
	default:
		return infoCmd("usage: /telegram [disconnect]")
	}
}

func renderConfig(cfg map[string]string) string {
	if len(cfg) == 0 {
		return "(no config set) — e.g. /config set telegram.chat_id 123456789"
	}
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString("Config:\n")
	for _, k := range keys {
		fmt.Fprintf(&sb, "  %s = %s\n", k, cfg[k])
	}
	return strings.TrimRight(sb.String(), "\n")
}
