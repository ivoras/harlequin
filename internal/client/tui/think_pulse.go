package tui

import (
	"fmt"
	"math"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Thinking-phase header pulse: bright yellow ↔ dark orange (xterm-friendly targets).
const (
	thinkYellowRGB = 0xFFFF00
	thinkOrangeRGB = 0xD78700
	thinkPulsePeriod = 1600 * time.Millisecond
	thinkPulseEvery  = 80 * time.Millisecond
)

type thinkPulseMsg struct{}

func thinkPulseTick() tea.Cmd {
	return tea.Tick(thinkPulseEvery, func(time.Time) tea.Msg { return thinkPulseMsg{} })
}

// modelThinking is true while the header thinking indicator is shown (in-flight request).
func (m *Model) modelThinking() bool {
	return m.loading
}

func thinkingPulseColor(now time.Time) string {
	phase := float64(now.UnixNano()%int64(thinkPulsePeriod)) / float64(thinkPulsePeriod)
	t := (math.Sin(phase*2*math.Pi) + 1) / 2
	return blendRGB(thinkYellowRGB, thinkOrangeRGB, t)
}

func blendRGB(c1, c2 uint32, t float64) string {
	r1, g1, b1 := uint8(c1>>16), uint8(c1>>8), uint8(c1)
	r2, g2, b2 := uint8(c2>>16), uint8(c2>>8), uint8(c2)
	r := uint8(float64(r1)*(1-t) + float64(r2)*t)
	g := uint8(float64(g1)*(1-t) + float64(g2)*t)
	b := uint8(float64(b1)*(1-t) + float64(b2)*t)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}
