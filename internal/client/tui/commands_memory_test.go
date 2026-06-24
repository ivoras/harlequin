package tui

import (
	"strings"
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

func TestFormatMemorySlotKeys(t *testing.T) {
	t.Parallel()
	if got := formatMemorySlotKeys([]types.MemorySlot{{Key: "company.name"}}); got != "{company.name}" {
		t.Fatalf("got %q", got)
	}
	if got := formatMemorySlotKeys([]types.MemorySlot{{Key: "memory.date"}, {Key: "user.birthday"}}); got != "{memory.date, user.birthday}" {
		t.Fatalf("got %q", got)
	}
	if got := formatMemorySlotKeys(nil); got != "{-}" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderMemoryLineSameFormat(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 5, 31, 21, 48, 0, 0, time.UTC)
	shared := types.Memory{
		ID: "s.3", Scope: "shared", Source: "auto",
		Slots: []types.MemorySlot{{Key: "organization.name", Value: "MegaCorp LLC"}}, Content: "The organization name is MegaCorp LLC.",
		CreatedAt: created,
	}
	user := types.Memory{
		ID: "u.1", Scope: "user", Source: "auto",
		Content:   "User prefers tea.",
		CreatedAt: created,
	}
	sharedLine := renderMemoryLine(shared, true)
	userLine := renderMemoryLine(user, false)
	if !strings.Contains(sharedLine, "{organization.name}") {
		t.Fatalf("shared line missing slot: %q", sharedLine)
	}
	if !strings.Contains(userLine, "{-}") {
		t.Fatalf("user line missing empty slot placeholder: %q", userLine)
	}
	// Same structural fields: pin, id width, timestamp, [scope/source], slot brace group.
	if strings.Count(sharedLine, "[") != 1 || strings.Count(userLine, "[") != 1 {
		t.Fatal("expected one [scope/source] group per line")
	}
}
