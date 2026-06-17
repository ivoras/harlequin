package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// exportSession writes the visible transcript to session_YYYYMMDD_HHMM.md in cwd.
// When raw is true everything is exported (thinking, tools, status, etc.);
// otherwise only the User and Assistant sections are kept.
func (m *Model) exportSession(raw bool) (string, error) {
	blocks := m.sessionBlocksForExport()
	if !raw {
		blocks = onlySession(blocks)
	}
	if len(blocks) == 0 {
		return "", fmt.Errorf("nothing to export")
	}
	md := formatSessionMarkdown(m, blocks)
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("session_%s.md", time.Now().Format("20060102_1504"))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (m *Model) sessionBlocksForExport() []roleBlock {
	out := make([]roleBlock, len(m.blocks))
	copy(out, m.blocks)
	if m.cfg.ShowThinking && m.streamingThinking.Len() > 0 {
		out = append(out, roleBlock{role: "thinking", text: m.streamingThinking.String()})
	}
	if m.streaming.Len() > 0 {
		out = append(out, roleBlock{role: "assistant", text: m.streaming.String()})
	}
	return out
}

// onlySession keeps just the User and Assistant blocks (drops thinking,
// tool calls, status, info, errors) for the default, non-raw export.
func onlySession(blocks []roleBlock) []roleBlock {
	out := make([]roleBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.role == "user" || b.role == "assistant" {
			out = append(out, b)
		}
	}
	return out
}

func formatSessionMarkdown(m *Model, blocks []roleBlock) string {
	var sb strings.Builder
	now := time.Now().Format("2006-01-02 15:04")
	sb.WriteString("# Harlequin session\n\n")
	fmt.Fprintf(&sb, "- **Exported:** %s\n", now)
	if m.user != nil {
		fmt.Fprintf(&sb, "- **User:** %s\n", m.user.Email)
	}
	if m.sessionID != 0 {
		fmt.Fprintf(&sb, "- **Session ID:** %d\n", m.sessionID)
	}
	if m.currentHat != "" {
		fmt.Fprintf(&sb, "- **Hat:** %s\n", m.currentHat)
	}
	if m.cfg.ServerURL != "" {
		fmt.Fprintf(&sb, "- **Server:** %s\n", m.cfg.ServerURL)
	}
	sb.WriteString("\n---\n\n")

	for _, b := range blocks {
		sb.WriteString(formatExportBlock(b))
		if !strings.HasSuffix(b.text, "\n") {
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n") + "\n"
}

func formatExportBlock(b roleBlock) string {
	text := strings.TrimRight(b.text, "\n")
	switch b.role {
	case "user":
		return "## You\n\n" + text + "\n"
	case "assistant":
		return "## Assistant\n\n" + text + "\n"
	case "thinking":
		return "### Thinking\n\n" + markdownBlockquote(text) + "\n"
	case "tool":
		return formatExportTool(text)
	case "error":
		return "## Error\n\n> " + strings.ReplaceAll(text, "\n", "\n> ") + "\n"
	case "status":
		return "*" + text + "*\n\n"
	case "info":
		return "> " + strings.ReplaceAll(text, "\n", "\n> ") + "\n\n"
	default:
		return text + "\n"
	}
}

func formatExportTool(text string) string {
	lines := strings.Split(text, "\n")
	var sb strings.Builder
	sb.WriteString("## Tool\n\n")
	for i, line := range lines {
		line = strings.TrimRight(line, " ")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "  ↳") {
			result := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "  ↳"), "↳"))
			fmt.Fprintf(&sb, "\n**Result:**\n\n> %s\n\n", strings.ReplaceAll(result, "\n", "\n> "))
			continue
		}
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("```text\n")
		sb.WriteString(strings.TrimPrefix(line, "⚙ "))
		sb.WriteString("\n```\n")
	}
	return sb.String()
}

func markdownBlockquote(s string) string {
	if strings.TrimSpace(s) == "" {
		return "> \n"
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}
