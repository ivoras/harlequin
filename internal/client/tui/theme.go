package tui

import "charm.land/lipgloss/v2"

// Styles holds the reusable lipgloss styles.
type Styles struct {
	Header           lipgloss.Style
	Status           lipgloss.Style
	User             lipgloss.Style
	UserArg          lipgloss.Style
	Assistant        lipgloss.Style
	Tool             lipgloss.Style
	ToolOutput       lipgloss.Style
	Thinking         lipgloss.Style
	InputBox         lipgloss.Style
	Help             lipgloss.Style
	Error            lipgloss.Style
	Accent           lipgloss.Style
	ContextOK        lipgloss.Style
	ContextWarn      lipgloss.Style
	ContextCritical  lipgloss.Style
	ContextMuted     lipgloss.Style
	Selected         lipgloss.Style
}

func newStyles() Styles {
	return Styles{
		Header: lipgloss.NewStyle().
			Foreground(colorBG).Background(colorAccent).Bold(true).Padding(0, 1),
		Status:     lipgloss.NewStyle().Foreground(colorMuted),
		User:       lipgloss.NewStyle().Foreground(colorWarm).Bold(true),
		UserArg:    lipgloss.NewStyle().Foreground(colorWarm),
		Assistant:  lipgloss.NewStyle().Foreground(colorText),
		Tool:       lipgloss.NewStyle().Foreground(colorMuted).Italic(true),
		ToolOutput: lipgloss.NewStyle().Foreground(colorMuted),
		Thinking:   lipgloss.NewStyle().Foreground(colorBG).Background(colorViolet).Italic(true),
		InputBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).Padding(0, 1),
		Help:            lipgloss.NewStyle().Foreground(colorMuted),
		Error:           lipgloss.NewStyle().Foreground(colorError).Bold(true),
		Accent:          lipgloss.NewStyle().Foreground(colorAccent),
		ContextOK:       lipgloss.NewStyle().Foreground(colorAccentHi),
		ContextWarn:     lipgloss.NewStyle().Foreground(colorWarm),
		ContextCritical: lipgloss.NewStyle().Foreground(colorError),
		ContextMuted:    lipgloss.NewStyle().Foreground(colorMuted),
		Selected:        lipgloss.NewStyle().Foreground(colorBG).Background(colorAccent).Bold(true),
	}
}
