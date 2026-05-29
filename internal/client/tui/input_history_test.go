package tui

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
)

func TestInputHistoryPushDedupes(t *testing.T) {
	t.Parallel()
	m := &Model{}
	m.pushInputHistory("hello")
	m.pushInputHistory("hello")
	if len(m.inputHistory) != 1 {
		t.Fatalf("len=%d want 1", len(m.inputHistory))
	}
}

func TestInputHistoryRecall(t *testing.T) {
	t.Parallel()
	m := &Model{input: textarea.New()}
	m.pushInputHistory("/memory")
	m.pushInputHistory("/skills")
	m.resetInputHistoryNav()

	if !m.tryRecallHistory(-1) {
		t.Fatal("expected up to recall")
	}
	if got := m.input.Value(); got != "/skills" {
		t.Fatalf("got %q", got)
	}
	if !m.tryRecallHistory(-1) {
		t.Fatal("expected second up")
	}
	if got := m.input.Value(); got != "/memory" {
		t.Fatalf("got %q", got)
	}
	if !m.tryRecallHistory(1) {
		t.Fatal("expected down")
	}
	if got := m.input.Value(); got != "/skills" {
		t.Fatalf("got %q", got)
	}
}

func TestShouldUseInputHistoryMultiLine(t *testing.T) {
	t.Parallel()
	m := &Model{input: textarea.New()}
	m.inputHistory = []string{"prev"}
	m.resetInputHistoryNav()
	m.input.SetValue("line1\nline2")

	if m.shouldUseInputHistory(-1) {
		t.Fatal("history disabled while editing multi-line draft")
	}
	m.input.SetValue("single")
	if !m.shouldUseInputHistory(-1) {
		t.Fatal("up on single line should recall")
	}
}
