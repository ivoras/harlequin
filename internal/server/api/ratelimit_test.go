package api

import "testing"

func TestRateLimiter(t *testing.T) {
	var l rateLimiter
	for i := 0; i < 3; i++ {
		if !l.allow("k", 3) {
			t.Fatalf("attempt %d should pass", i+1)
		}
	}
	if l.allow("k", 3) {
		t.Fatal("4th attempt should be blocked")
	}
	if !l.allow("other", 3) {
		t.Fatal("a different key is independent")
	}
}
