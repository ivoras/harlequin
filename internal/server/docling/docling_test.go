package docling

import "testing"

func TestDescribeErrors(t *testing.T) {
	got := describeErrors([]any{map[string]any{"component_type": "user_input", "error_message": "An unexpected error\noccurred   while opening x.xls"}})
	if got != "An unexpected error occurred while opening x.xls" {
		t.Errorf("got %q", got)
	}
	if got := describeErrors([]any{"odd"}); got != `"odd"` {
		t.Errorf("non-object error: %q", got)
	}
	if describeErrors(nil) != "no error details" {
		t.Error("empty list")
	}
}
