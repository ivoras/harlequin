package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	mdHeading = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mdBullet  = lipgloss.NewStyle().Foreground(colorAccentHi)
	mdCode    = lipgloss.NewStyle().Foreground(colorAccentHi).Background(colorSurface)
)

// renderMarkdownish applies light, dependency-free markdown styling: headings,
// bullets, and inline/fenced code. It is intentionally simple (glamour pulls a
// conflicting dependency tree in this project).
func renderMarkdownish(s string) string {
	lines := strings.Split(s, "\n")
	inFence := false
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			out = append(out, mdCode.Render(line))
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "#"):
			out = append(out, mdHeading.Render(strings.TrimLeft(trimmed, "# ")))
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			out = append(out, mdBullet.Render("•")+" "+trimmed[2:])
		default:
			out = append(out, renderInline(line))
		}
	}
	return strings.Join(out, "\n")
}

// renderInline styles `inline code` spans.
func renderInline(line string) string {
	if !strings.Contains(line, "`") {
		return line
	}
	parts := strings.Split(line, "`")
	var sb strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			sb.WriteString(mdCode.Render(p))
		} else {
			sb.WriteString(p)
		}
	}
	return sb.String()
}
