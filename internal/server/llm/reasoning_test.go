package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Per-model options shape the request: reasoning_effort is sent when set (not
// with DisableThinking), and earlier reasoning goes back unless dropped.
func TestModelOptionsInRequest(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	p := NewOpenAICompatible("chat", srv.URL, "", "keep")
	p.SetModelOptions(map[string]ModelOptions{"keep": {ReasoningEffort: "high"}, "drop": {DropReasoning: true}})
	msgs := []Message{{Role: RoleUser, Content: "q"}, {Role: RoleAssistant, Content: "a", ReasoningContent: "why"}}
	send := func(req ChatRequest) (effort any, reasoning any) {
		t.Helper()
		ch, err := p.Chat(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		drain(t, ch)
		return got["reasoning_effort"], got["messages"].([]any)[1].(map[string]any)["reasoning_content"]
	}
	if e, r := send(ChatRequest{Model: "keep", Messages: msgs}); e != "high" || r != "why" {
		t.Fatalf("keep: effort=%v reasoning=%v", e, r)
	}
	if e, _ := send(ChatRequest{Model: "keep", Messages: msgs, DisableThinking: true}); e != nil {
		t.Fatalf("DisableThinking still sent effort %v", e)
	}
	if e, r := send(ChatRequest{Model: "drop", Messages: msgs}); e != nil || r != nil {
		t.Fatalf("drop: effort=%v reasoning=%v", e, r)
	}
	if msgs[1].ReasoningContent != "why" {
		t.Fatal("dropping changed the caller's messages")
	}
}
