package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

func newAskModel(items []askItem) *Model {
	m := &Model{pendingAsk: items, styles: newStyles(), width: 80}
	m.input = textarea.New()
	m.enterAsk()
	return m
}

func TestAskNavigateAndRecord(t *testing.T) {
	m := newAskModel([]askItem{
		{question: "Favorite color?", options: []string{"red", "green", "blue"}},
		{question: "Size?", options: []string{"S", "M"}},
	})
	// down to "green", enter selects it and advances to question 2.
	m.handleAskKey(tea.KeyPressMsg{}, "down")
	if m.askSel != 1 {
		t.Fatalf("askSel = %d, want 1", m.askSel)
	}
	m.handleAskKey(tea.KeyPressMsg{}, "enter")
	if m.askIndex != 1 || m.askSel != 0 {
		t.Fatalf("after select: index=%d sel=%d", m.askIndex, m.askSel)
	}
	if len(m.askAnswers) != 1 || m.askAnswers[0] != "green" {
		t.Fatalf("answers=%v", m.askAnswers)
	}
}

func TestAskWrapAndOther(t *testing.T) {
	m := newAskModel([]askItem{{question: "Name?", options: []string{"Alice"}}})
	// total entries = 1 option + Other = 2. up from 0 wraps to 1 (Other).
	m.handleAskKey(tea.KeyPressMsg{}, "up")
	if m.askSel != 1 {
		t.Fatalf("askSel = %d, want 1 (Other)", m.askSel)
	}
	m.handleAskKey(tea.KeyPressMsg{}, "enter")
	if !m.askOther {
		t.Fatal("expected free-text (Other) mode")
	}
}

func TestBuildAskAnswer(t *testing.T) {
	d, s := buildAskAnswer([]askItem{{question: "Proceed?"}}, []string{"yes"})
	if d != "yes" || s != "yes" {
		t.Fatalf("single: display=%q send=%q", d, s)
	}
	d, s = buildAskAnswer([]askItem{{question: "Color?"}, {question: "Size?"}}, []string{"red", "M"})
	if !strings.Contains(d, "1. red") || !strings.Contains(d, "2. M") {
		t.Fatalf("display=%q", d)
	}
	if !strings.Contains(s, "Color?") || !strings.Contains(s, "→ red") || !strings.Contains(s, "→ M") {
		t.Fatalf("send=%q", s)
	}
}

func TestRenderAskQuestions(t *testing.T) {
	if got := renderAskQuestions([]askItem{{question: "One?"}}); got != "One?" {
		t.Fatalf("single=%q", got)
	}
	got := renderAskQuestions([]askItem{{question: "One?"}, {question: "Two?"}})
	if !strings.Contains(got, "Asked:") || !strings.Contains(got, "One?") || !strings.Contains(got, "Two?") {
		t.Fatalf("multi=%q", got)
	}
}

func TestAskViewRenders(t *testing.T) {
	m := newAskModel([]askItem{
		{question: "Pick a color you like best for the theme?", options: []string{"red", "green", "blue"}},
		{question: "Confirm?", options: []string{"yes", "no"}},
	})
	out := m.askView()
	if !strings.Contains(out, "asked 2 questions") {
		t.Fatalf("missing multi-question warning:\n%s", out)
	}
	if !strings.Contains(out, askOtherLabel) {
		t.Fatalf("missing Other entry:\n%s", out)
	}
	// single-question view should not show the warning
	m2 := newAskModel([]askItem{{question: "Proceed?", options: []string{"yes", "no"}}})
	if strings.Contains(m2.askView(), "questions — answer") {
		t.Fatal("single question should not warn about multiple")
	}
}
