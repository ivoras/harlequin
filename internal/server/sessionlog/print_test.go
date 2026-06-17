package sessionlog

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintTrajectory(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"ts":"2026-05-29T12:00:00Z","session_id":1,"user_id":1,"turn":1,"step":0,"type":"tools_available","data":{"count":2,"tools":[{"name":"list_skills","description":"List skills"},{"name":"memory_search","description":"Search"}]}}`,
		`{"ts":"2026-05-29T12:00:01Z","session_id":1,"user_id":1,"turn":1,"step":1,"type":"tool_call","data":{"id":"call_1","name":"list_skills","args":{}}}`,
		`{"ts":"2026-05-29T12:00:01Z","session_id":1,"user_id":1,"turn":1,"step":1,"type":"tool_result","data":{"id":"call_1","name":"list_skills","ok":true,"output":"- greeter: Greet\n","duration_ms":5,"duration_ns":5123456,"output_bytes":16}}`,
	}, "\n")

	events, err := Read(strings.NewReader(jsonl))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	Print(&buf, events, PrintOptions{})
	out := buf.String()
	for _, want := range []string{"tools_available", "★ TOOL list_skills", "skill catalogue", "(5ms", "greeter"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}
