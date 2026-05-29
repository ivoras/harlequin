package tui

import "charm.land/lipgloss/v2"

// Palette centralizes the dark-purple / light-green theme. Tweak here.
var (
	colorBG       = lipgloss.Color("#1A1024") // deep dark purple
	colorSurface  = lipgloss.Color("#251636") // panels
	colorBorder   = lipgloss.Color("#3A2A52")
	colorAccent   = lipgloss.Color("#A6E22E") // light green
	colorAccentHi = lipgloss.Color("#C6F68D") // bright green
	colorText     = lipgloss.Color("#E6E0F0") // light lavender
	colorMuted    = lipgloss.Color("#8A7CA8") // muted purple
)

// Styles holds the reusable lipgloss styles.
type Styles struct {
	Header     lipgloss.Style
	Status     lipgloss.Style
	User       lipgloss.Style
	Assistant  lipgloss.Style
	Tool       lipgloss.Style
	ToolOutput lipgloss.Style
	Thinking   lipgloss.Style
	InputBox   lipgloss.Style
	Help       lipgloss.Style
	Error      lipgloss.Style
	Accent     lipgloss.Style
}

func newStyles() Styles {
	return Styles{
		Header: lipgloss.NewStyle().
			Foreground(colorBG).Background(colorAccent).Bold(true).Padding(0, 1),
		Status: lipgloss.NewStyle().Foreground(colorMuted),
		User: lipgloss.NewStyle().Foreground(colorAccentHi).Bold(true),
		Assistant: lipgloss.NewStyle().Foreground(colorText),
		Tool: lipgloss.NewStyle().Foreground(colorMuted).Italic(true),
		ToolOutput: lipgloss.NewStyle().Foreground(colorMuted),
		Thinking: lipgloss.NewStyle().Foreground(colorMuted).Italic(true),
		InputBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).Padding(0, 1),
		Help:   lipgloss.NewStyle().Foreground(colorMuted),
		Error:  lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B")).Bold(true),
		Accent: lipgloss.NewStyle().Foreground(colorAccent),
	}
}
