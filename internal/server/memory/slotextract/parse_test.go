package slotextract

import "testing"

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		in        string
		wantOK    bool
		wantKey   string
		wantValue string
	}{
		{"clean", `{"key":"company.name","value":"Woodchucks, Inc"}`, true, "company.name", "Woodchucks, Inc"},
		{"normalizes key", `{"key":"Company.Name!","value":"Acme"}`, true, "company.name", "Acme"},
		{"empty -> none", `{"key":"","value":""}`, false, "", ""},
		{"missing value", `{"key":"user.timezone","value":""}`, false, "", ""},
		{"fenced json", "```json\n{\"key\":\"x.y\",\"value\":\"v\"}\n```", true, "x.y", "v"},
		{"garbage", `not json`, false, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Parse(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v (%#v)", ok, tc.wantOK, got)
			}
			if ok && (got.Key != tc.wantKey || got.Value != tc.wantValue) {
				t.Fatalf("got %#v want key=%q value=%q", got, tc.wantKey, tc.wantValue)
			}
		})
	}
}

func TestBuildUserPrompt(t *testing.T) {
	t.Parallel()
	if got := BuildUserPrompt(nil, "x"); got == "" || !contains(got, "(none yet)") {
		t.Fatalf("empty keys: %q", got)
	}
	if got := BuildUserPrompt([]string{"a.b", "c.d"}, "x"); !contains(got, "a.b, c.d") {
		t.Fatalf("keys joined: %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
