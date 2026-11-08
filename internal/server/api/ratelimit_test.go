package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestClientIP(t *testing.T) {
	for _, c := range []struct{ remote, xri, want string }{
		{"127.0.0.1:5000", "203.0.113.7", "203.0.113.7"}, // behind local nginx
		{"[::1]:5000", "203.0.113.7", "203.0.113.7"},
		{"127.0.0.1:5000", "", "127.0.0.1"},
		{"127.0.0.1:5000", "not-an-ip", "127.0.0.1"},
		{"198.51.100.9:5000", "203.0.113.7", "198.51.100.9"}, // forged header ignored
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xri != "" {
			r.Header.Set("X-Real-IP", c.xri)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("%s xri=%q: got %s, want %s", c.remote, c.xri, got, c.want)
		}
	}
}

func TestLoginCountsOnlyFailures(t *testing.T) {
	s := &Server{}
	req := func(ip string) *http.Request {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("X-Real-IP", ip)
		return r
	}
	blocked := func(ip, email string) bool { return s.loginBlocked(httptest.NewRecorder(), req(ip), email) }

	// Many successful logins from one office IP / one email never block.
	for i := 0; i < 100; i++ {
		if blocked("203.0.113.7", "a@x.com") {
			t.Fatalf("blocked after %d successes", i)
		}
		s.loginSucceeded(req("203.0.113.7"), "a@x.com")
	}
	// Failures block that email, only for that email and only on that IP's budget.
	for i := 0; i < loginMax; i++ {
		s.loginFailed(req("203.0.113.7"), "a@x.com")
	}
	w := httptest.NewRecorder()
	if !s.loginBlocked(w, req("203.0.113.7"), "a@x.com") || w.Code != 429 || w.Header().Get("Retry-After") != "900" {
		t.Fatalf("want 429+Retry-After, got %d", w.Code)
	}
	if blocked("203.0.113.7", "b@x.com") {
		t.Fatal("other email behind the same IP must still log in")
	}
	// The attacker's other IP is also locked out of that email; a success clears it.
	s.loginSucceeded(req("203.0.113.7"), "a@x.com")
	if blocked("203.0.113.7", "a@x.com") {
		t.Fatal("success should clear the email's failures")
	}
}
