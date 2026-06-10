package tui

import (
	"fmt"
	"strings"
)

// slashCommands are the top-level commands offered by the autocomplete menu.
var slashCommands = []string{
	"/config", "/cron", "/docs", "/export", "/hat", "/help", "/mcp", "/memory", "/new",
	"/quit", "/reload", "/resume", "/skill", "/skills", "/usage",
}

var slashHelp = map[string]string{
	"/config": "view / set per-user config (e.g. Telegram)",
	"/cron":   "list / add / manage scheduled jobs",
	"/docs":   "search org documents; '/docs add <path>' uploads a file (e.g. PDF)",
	"/export": "save transcript (User+Assistant; add 'raw' for everything)",
	"/hat":    "list / show / wear hats",
	"/help":   "show help",
	"/mcp":    "list / add / test / authorize MCP servers",
	"/memory": "list / show / manage memories",
	"/new":    "start a new conversation",
	"/quit":   "exit",
	"/reload": "(admin) re-read skill/prompt/hat files",
	"/resume": "list recent conversations",
	"/skill":  "pull / push / reset / diff / new a skill",
	"/skills": "list available skills",
	"/usage":  "show token/cost usage",
}

// slashSuggestions returns the commands matching the input when a slash command
// is being typed at the start of the line (a leading "/", no space yet). It is
// empty otherwise — so the menu only appears for a slash at char 1.
func (m *Model) slashSuggestions() []string {
	return matchSlashCommands(m.input.Value())
}

// matchSlashCommands returns the commands the value is a prefix of, but only
// when the value is a slash being typed at the start of the line (no space).
func matchSlashCommands(v string) []string {
	if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, " \t\n") {
		return nil
	}
	lv := strings.ToLower(v)
	var out []string
	for _, c := range slashCommands {
		if strings.HasPrefix(c, lv) {
			out = append(out, c)
		}
	}
	return out
}

// isExactSlashCommand reports whether v is exactly a known command.
func isExactSlashCommand(v string) bool {
	if v == "/exit" {
		return true
	}
	for _, c := range slashCommands {
		if c == v {
			return true
		}
	}
	return false
}

// completeSlash replaces the input with the chosen command plus a trailing
// space (which closes the menu and lets the user type any arguments).
func (m *Model) completeSlash(cmd string) {
	m.input.SetValue(cmd + " ")
	m.input.CursorEnd()
	m.slashSel = 0
}

// clampSlashSel keeps the selection within the current suggestions.
func clampSlashSel(sel, n int) int {
	if n == 0 {
		return 0
	}
	if sel < 0 {
		return 0
	}
	if sel >= n {
		return n - 1
	}
	return sel
}

// renderSlashMenuLines renders the autocomplete dropdown as styled lines (nil
// when no menu should show).
func (m *Model) renderSlashMenuLines() []string {
	sugg := m.slashSuggestions()
	if len(sugg) == 0 {
		return nil
	}
	sel := clampSlashSel(m.slashSel, len(sugg))
	width := m.width - 1
	lines := make([]string, len(sugg))
	for i, c := range sugg {
		marker := "   "
		if i == sel {
			marker = " ▸ "
		}
		text := truncate(fmt.Sprintf("%s%-9s %s", marker, c, slashHelp[c]), width)
		if i == sel {
			lines[i] = m.styles.Accent.Render(text)
		} else {
			lines[i] = m.styles.Help.Render(text)
		}
	}
	return lines
}

// overlayBottomLines replaces the last len(menu) lines of view with menu,
// keeping the total line count unchanged so the layout does not grow.
func overlayBottomLines(view string, menu []string) string {
	if len(menu) == 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	start := len(lines) - len(menu)
	if start < 0 {
		start = 0
	}
	for i := 0; i < len(menu); i++ {
		idx := start + i
		if idx >= 0 && idx < len(lines) {
			lines[idx] = menu[i]
		}
	}
	return strings.Join(lines, "\n")
}
