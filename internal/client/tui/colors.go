package tui

import (
	"image/color"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Harlequin palette: canonical RGB hex values. Terminals without truecolor get
// the nearest xterm-256 / 16-color match via lipgloss.Complete (see palette).
const (
	hexBG       = "#1c1c1c"
	hexSurface  = "#303030"
	hexBorder   = "#444444"
	hexText     = "#dadada"
	hexMuted    = "#8a8a8a"
	hexAccent   = "#5fd7af"
	hexAccentHi = "#87ffd7"
	hexWarm     = "#ffaf87"
	hexWarmHi   = "#ffd7af"
	hexViolet   = "#8787af"
	hexError    = "#ff5f5f"
)

var colorComplete = lipgloss.Complete(colorprofile.Detect(os.Stdout, os.Environ()))

// palette picks truecolor, 256-color, or 16-color based on the terminal profile.
func palette(hex, ansi256, ansi string) color.Color {
	return colorComplete(lipgloss.Color(ansi), lipgloss.Color(ansi256), lipgloss.Color(hex))
}

// Theme colors (used by lipgloss styles and the Bubble Tea alt-screen background).
var (
	colorBG       = palette(hexBG, "234", "0")
	colorSurface  = palette(hexSurface, "236", "8")
	colorBorder   = palette(hexBorder, "238", "8")
	colorText     = palette(hexText, "253", "7")
	colorMuted    = palette(hexMuted, "245", "8")
	colorAccent   = palette(hexAccent, "79", "6")
	colorAccentHi = palette(hexAccentHi, "122", "14")
	colorWarm     = palette(hexWarm, "216", "3")
	colorWarmHi   = palette(hexWarmHi, "223", "7")
	colorViolet   = palette(hexViolet, "103", "5")
	colorError    = palette(hexError, "203", "1")
)
