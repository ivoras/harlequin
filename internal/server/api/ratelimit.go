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

func (l *rateLimiter) allow(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	if len(l.hits) > 10000 { // bound memory: drop idle keys
		for k, ts := range l.hits {
			if now.Sub(ts[len(ts)-1]) > authWindow {
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
	if len(recent) >= max {
		l.hits[key] = recent
		return false
	}
	l.hits[key] = append(recent, now)
	return true
}

// limited counts one attempt for this client IP and (if given) email under the
// named route and answers 429 when either is over its limit.
func (s *Server) limited(w http.ResponseWriter, r *http.Request, route, email string, max int) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	ok := s.limiter.allow(route+"|ip|"+ip, max*3) // an office may share one IP
	if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
		ok = s.limiter.allow(route+"|email|"+email, max) && ok
	}
	if !ok {
		w.Header().Set("Retry-After", "900")
		writeErr(w, http.StatusTooManyRequests, "too many attempts — try again in 15 minutes")
	}
	return !ok
}
