package tui

import (
	"testing"
	"time"
)

func TestFormatMemoryTime(t *testing.T) {
	t.Parallel()
	got := formatMemoryTime(time.Date(2026, 5, 29, 14, 35, 42, 0, time.FixedZone("CET", 3600)))
	if got != "2026-05-29T13:35Z" {
		t.Fatalf("got %q", got)
	}
}
