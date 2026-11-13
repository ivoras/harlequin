package tui

import (
	"testing"

	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestFormatHatHint(t *testing.T) {
	if got := formatHatHint(nil); got != "" {
		t.Errorf("no hats: %q", got)
	}
	got := formatHatHint([]types.Hat{{Name: "analyst"}, {Name: "researcher"}})
	if want := "hats: analyst, researcher — /hat wear <name>"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
