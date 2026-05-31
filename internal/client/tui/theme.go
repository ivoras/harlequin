package tui

import "charm.land/lipgloss/v2"

// Palette: a calm teal + warm apricot duotone on a charcoal canvas. Defined with
// xterm-256 palette indices (the client requires a 256-color terminal), so the
// scheme renders consistently without relying on truecolor. Approximate hex is
// noted for reference. Tweak here.
var (
	colorBG       = lipgloss.Color("234") // #1c1c1c  charcoal canvas
	colorSurface  = lipgloss.Color("236") // #303030  panels / code background
	colorBorder   = lipgloss.Color("238") // #444444  subtle borders
	colorText     = lipgloss.Color("253") // #dadada  soft light grey (body text)
	colorMuted    = lipgloss.Color("245") // #8a8a8a  secondary / chrome text
	colorAccent   = lipgloss.Color("79")  // #5fd7af  soft teal (primary accent)
	colorAccentHi = lipgloss.Color("122") // #87ffd7  mint (highlights, md bullets/code)
	colorWarm     = lipgloss.Color("216") // #ffaf87  apricot (the user's own lines)
	colorWarmHi   = lipgloss.Color("223") // #ffd7af  sand (warm highlight)
	colorViolet   = lipgloss.Color("103") // #8787af  soft lavender (reasoning)
	colorError    = lipgloss.Color("203") // #ff5f5f  soft red
)

// Styles holds the reusable lipgloss styles.
type Styles struct {
	Header           lipgloss.Style
	Status           lipgloss.Style
	User             lipgloss.Style
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
}

func newStyles() Styles {
	return Styles{
		Header: lipgloss.NewStyle().
			Foreground(colorBG).Background(colorAccent).Bold(true).Padding(0, 1),
		Status:     lipgloss.NewStyle().Foreground(colorMuted),
		User:       lipgloss.NewStyle().Foreground(colorWarm).Bold(true),
		Assistant:  lipgloss.NewStyle().Foreground(colorText),
		Tool:       lipgloss.NewStyle().Foreground(colorMuted).Italic(true),
		ToolOutput: lipgloss.NewStyle().Foreground(colorMuted),
		Thinking:   lipgloss.NewStyle().Foreground(colorViolet).Italic(true),
		InputBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).Padding(0, 1),
		Help:   lipgloss.NewStyle().Foreground(colorMuted),
		Error:  lipgloss.NewStyle().Foreground(colorError).Bold(true),
		Accent:          lipgloss.NewStyle().Foreground(colorAccent),
		ContextOK:       lipgloss.NewStyle().Foreground(colorAccentHi),
		ContextWarn:     lipgloss.NewStyle().Foreground(colorWarm),
		ContextCritical: lipgloss.NewStyle().Foreground(colorError),
		ContextMuted:    lipgloss.NewStyle().Foreground(colorMuted),
	}
}
