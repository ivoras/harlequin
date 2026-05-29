package llm

import (
	"encoding/json"
	"testing"
)

func TestNormalizeThinking(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"reasoning_content", `{"reasoning_content":"step 1"}`, "step 1"},
		{"reasoning", `{"reasoning":"r"}`, "r"},
		{"thinking", `{"thinking":"t"}`, "t"},
		{"thought", `{"thought":"th"}`, "th"},
		{"priority", `{"thinking":"t","reasoning_content":"rc"}`, "rc"},
		{"content only", `{"content":"hello"}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal([]byte(tc.json), &m); err != nil {
				t.Fatal(err)
			}
			if got := normalizeThinking(m); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestJSONDeltaUnmarshalThinking(t *testing.T) {
	raw := `{"reasoning_content":"plan","content":"hi","tool_calls":[]}`
	var d jsonDelta
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	if got := normalizeThinking(d.extra); got != "plan" {
		t.Fatalf("thinking: got %q", got)
	}
	if d.Content != "hi" {
		t.Fatalf("content: got %q", d.Content)
	}
}
