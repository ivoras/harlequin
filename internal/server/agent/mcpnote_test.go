package agent

import (
	"strings"
	"testing"
)

func TestMCPNeedAuthNote(t *testing.T) {
	n := mcpNeedAuthNote([]string{"shared/linear", "user/notion"})
	if !strings.Contains(n, "shared/linear, user/notion") || !strings.Contains(n, "/mcp auth") {
		t.Fatalf("note missing servers or TUI hint: %s", n)
	}
}
