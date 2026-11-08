package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// rateLimiter is a sliding-window counter for the unauthenticated auth routes.
// In memory only: a restart resets it, and it is per-process (one server).
type rateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

const authWindow = 15 * time.Minute

// prune drops expired hits for key and returns the live ones. Caller holds mu.
func (l *rateLimiter) prune(key string, now time.Time) []time.Time {
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	if len(l.hits) > 10000 { // bound memory: drop idle keys
		for k, ts := range l.hits {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > authWindow {
				delete(l.hits, k)
			}
		}
	}
	recent := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < authWindow {
			recent = append(recent, t)
		}
	}
	l.hits[key] = recent
	return recent
}

// allow records one attempt and reports whether key is still within max.
func (l *rateLimiter) allow(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	recent := l.prune(key, now)
	if len(recent) >= max {
		return false
	}
	l.hits[key] = append(recent, now)
	return true
}

// over reports whether key has reached max, without recording anything.
func (l *rateLimiter) over(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key, time.Now())) >= max
}

func (l *rateLimiter) add(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.hits[key] = append(l.prune(key, now), now)
}

func (l *rateLimiter) clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}

// clientIP is the caller's address. Behind a reverse proxy on this host (nginx
// on loopback) RemoteAddr is always 127.0.0.1, so X-Real-IP is honoured, but
// only when the direct peer is loopback: from anyone else the header is forgeable.
func clientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if p := net.ParseIP(ip); p != nil && p.IsLoopback() {
		if real := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); real != nil {
			return real.String()
		}
	}
	return ip
}

func reject(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "900")
	writeErr(w, http.StatusTooManyRequests, "too many attempts — try again in 15 minutes")
}

// limited counts one attempt for this client IP and (if given) email under the
// named route and answers 429 when either is over its limit. Used where every
// attempt should cost budget (register, verify); logins use loginBlocked.
func (s *Server) limited(w http.ResponseWriter, r *http.Request, route, email string, max int) bool {
	ok := s.limiter.allow(route+"|ip|"+clientIP(r), max*3) // an office may share one IP
	if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
		ok = s.limiter.allow(route+"|email|"+email, max) && ok
	}
	if !ok {
		reject(w)
	}
	return !ok
}

const loginMax = 10

func loginKeys(r *http.Request, email string) (ipKey, emailKey string) {
	return "login|ip|" + clientIP(r), "login|email|" + strings.ToLower(strings.TrimSpace(email))
}

// loginBlocked answers 429 (and returns true) when the IP or email has used up
// its failed-login budget. It records nothing, and runs before the password is
// checked so a locked account is rejected without verifying it.
func (s *Server) loginBlocked(w http.ResponseWriter, r *http.Request, email string) bool {
	ipKey, emailKey := loginKeys(r, email)
	if s.limiter.over(ipKey, loginMax*3) || s.limiter.over(emailKey, loginMax) {
		reject(w)
		return true
	}
	return false
}

// loginFailed charges one wrong-credentials attempt to the IP and the email.
func (s *Server) loginFailed(r *http.Request, email string) {
	ipKey, emailKey := loginKeys(r, email)
	s.limiter.add(ipKey)
	s.limiter.add(emailKey)
}

// loginSucceeded forgets the email's earlier failures (typos before success).
// The IP's count stays: it is shared by everyone behind that address.
func (s *Server) loginSucceeded(r *http.Request, email string) {
	_, emailKey := loginKeys(r, email)
	s.limiter.clear(emailKey)
}
