package tui

import (
	_ "embed"
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

//go:embed glamour_harlequin.json
var glamourHarlequinStyle []byte

var (
	mdRenderer   *glamour.TermRenderer
	mdRendererW  int
	mdRendererMu sync.Mutex

	mdHeading = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mdBullet  = lipgloss.NewStyle().Foreground(colorAccentHi)
	mdCode    = lipgloss.NewStyle().Foreground(colorAccentHi).Background(colorSurface)
)

// renderMarkdown renders assistant Markdown for the terminal (GFM: bold, lists,
// blockquotes, fenced code with chroma, tables). Falls back to renderMarkdownish
// if glamour fails.
func renderMarkdown(width int, s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	mdRendererMu.Lock()
	defer mdRendererMu.Unlock()
	r, err := markdownRendererLocked(width)
	if err != nil {
		return renderMarkdownish(s)
	}
	out, err := r.Render(s)
	if err != nil {
		return renderMarkdownish(s)
	}
	return strings.TrimRight(out, "\n")
}

func markdownRendererLocked(width int) (*glamour.TermRenderer, error) {
	if mdRenderer != nil && mdRendererW == width {
		return mdRenderer, nil
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(glamourHarlequinStyle),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil, err
	}
	mdRenderer = r
	mdRendererW = width
	return r, nil
}

// renderMarkdownish is a lightweight fallback when glamour cannot run.
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
		case strings.HasPrefix(trimmed, "> "):
			out = append(out, lipgloss.NewStyle().Foreground(colorMuted).Render("│ "+strings.TrimPrefix(trimmed, "> ")))
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			out = append(out, mdBullet.Render("•")+" "+renderInline(trimmed[2:]))
		default:
			out = append(out, renderInline(line))
		}
	}
	return strings.Join(out, "\n")
}

// renderInline styles `inline code` and **bold** spans (fallback path only).
func renderInline(line string) string {
	if strings.Contains(line, "`") {
		line = renderInlineCode(line)
	}
	if strings.Contains(line, "**") {
		line = renderInlineBold(line)
	}
	return line
}

func renderInlineCode(line string) string {
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

func renderInlineBold(line string) string {
	parts := strings.Split(line, "**")
	var sb strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			sb.WriteString(lipgloss.NewStyle().Foreground(colorAccentHi).Bold(true).Render(p))
		} else {
			sb.WriteString(p)
		}
	}
	return sb.String()
}
