package tui

import (
	"strings"
	"testing"
)

// /project assign moves the current PERSONAL session; inside a project the
// current session is a project session, so it must refuse (sending that id
// could move an unrelated personal session with the same number).
func TestProjectAssignRefusesInsideProject(t *testing.T) {
	m := &Model{activeProjectID: 3, sessionID: 7}
	msg := m.runProject([]string{"assign", "other"})()
	info, ok := msg.(infoMsg)
	if !ok || !strings.Contains(info.text, "leave the project first") {
		t.Fatalf("got %#v", msg)
	}
	m = &Model{sessionID: 7}
	if info, ok := m.runProject([]string{"assign"})().(infoMsg); !ok || !strings.Contains(info.text, "usage") {
		t.Fatalf("missing project should print usage, got %#v", info)
	}
}
