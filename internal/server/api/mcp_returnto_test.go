package api

import "testing"

func TestSafeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"/m/?s=3":             "/m/?s=3",
		"//evil.com":          "",
		"https://evil.com":    "",
		"/\\evil.com":         "",
		"/x\r\nSet-Cookie: a": "",
		"":                    "",
	} {
		if got := safeReturnTo(in); got != want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}
