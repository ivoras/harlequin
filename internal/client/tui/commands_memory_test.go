package tui

import (
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestFormatMemoryTime(t *testing.T) {
	t.Parallel()
	got := formatMemoryTime(time.Date(2026, 5, 29, 14, 35, 42, 0, time.FixedZone("CET", 3600)))
	if got != "2026-05-29T13:35Z" {
		t.Fatalf("got %q", got)
	}
}

func TestMemoryDeletable(t *testing.T) {
	t.Parallel()
	userMem := types.Memory{Scope: "user"}
	sharedMem := types.Memory{Scope: "shared"}
	if !memoryDeletable(userMem, false) {
		t.Fatal("user memory should be deletable")
	}
	if memoryDeletable(sharedMem, false) {
		t.Fatal("shared memory not deletable for regular user")
	}
	if !memoryDeletable(sharedMem, true) {
		t.Fatal("shared memory deletable for admin")
	}
}
