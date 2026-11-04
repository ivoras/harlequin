// Package loopdetect spots degenerate, self-repeating LLM output while it
// streams, so a runaway generation (typically a reasoning model going round in
// circles) can be cancelled instead of burning minutes of compute until the
// provider's own limit kills the connection.
//
// It is deliberately conservative: it only fires on long exact repeats that
// natural text essentially never contains, so a false positive costs little but
// a miss just falls back to the existing size cap.
package loopdetect

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

const (
	maxBuf     = 128 << 10 // rolling window examined (must hold minRepeats periods of a long loop)
	checkEvery = 2 << 10  // bytes of new text between checks
	minTotal   = 6 << 10  // never fire before this much output
	probeLen   = 240      // size of the tail slice searched for in the window
	minRepeats = 10       // occurrences of the probe that mean "looping"; reasoning that re-quotes a source passage 4-6 times is not a loop

	lineWindow   = 64 // non-trivial lines examined
	minLines     = 24
	maxDistinct  = 0.25 // distinct/total at or below this = repeated lines
	minLineChars = 12
)

// Detector inspects one stream (feed thinking and content to separate ones).
type Detector struct {
	buf        []byte
	total      int
	sinceCheck int
}

// Feed adds streamed text. It returns true, with a short excerpt of what is
// repeating, as soon as the output is judged to be looping.
func (d *Detector) Feed(s string) (bool, string) {
	d.buf = append(d.buf, s...)
	if len(d.buf) > maxBuf {
		d.buf = append(d.buf[:0], d.buf[len(d.buf)-maxBuf:]...)
	}
	d.total += len(s)
	d.sinceCheck += len(s)
	if d.total < minTotal || d.sinceCheck < checkEvery {
		return false, ""
	}
	d.sinceCheck = 0
	return d.check()
}

func (d *Detector) check() (bool, string) {
	// 1. A long tail passage that already occurs several times in the window.
	if len(d.buf) >= probeLen*minRepeats {
		start := len(d.buf) - probeLen
		for start < len(d.buf) && !utf8.RuneStart(d.buf[start]) {
			start++
		}
		probe := d.buf[start:]
		if len(bytes.TrimSpace(probe)) > 0 && bytes.Count(d.buf, probe) >= minRepeats {
			return true, string(probe)
		}
	}
	// 2. Mostly duplicate lines among the most recent ones.
	lines := recentLines(d.buf)
	if len(lines) >= minLines {
		seen := make(map[string]struct{}, len(lines))
		for _, l := range lines {
			seen[l] = struct{}{}
		}
		if float64(len(seen))/float64(len(lines)) <= maxDistinct {
			return true, lines[len(lines)-1]
		}
	}
	return false, ""
}

func recentLines(b []byte) []string {
	var out []string
	parts := strings.Split(string(b), "\n")
	for i := len(parts) - 1; i >= 0 && len(out) < lineWindow; i-- {
		l := strings.TrimSpace(parts[i])
		if len(l) >= minLineChars {
			out = append(out, l)
		}
	}
	return out
}
