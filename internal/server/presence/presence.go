// Package presence tracks which (user, interface) pairs have a live client, by
// recording the last time each made an authenticated request. Background tasks
// (e.g. the auto-titler) use it to only notify interfaces that are actually
// connected, instead of broadcasting to clients that aren't listening.
package presence

import (
	"fmt"
	"sync"
	"time"
)

// Tracker is a concurrency-safe last-seen map keyed by (userID, interface).
type Tracker struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// New constructs a Tracker.
func New() *Tracker { return &Tracker{seen: map[string]time.Time{}} }

func key(userID int64, iface string) string { return fmt.Sprintf("%d|%s", userID, iface) }

// Touch records that the given user's interface was just active.
func (t *Tracker) Touch(userID int64, iface string) {
	if t == nil || iface == "" {
		return
	}
	t.mu.Lock()
	t.seen[key(userID, iface)] = time.Now()
	t.mu.Unlock()
}

// Alive reports whether the user's interface has been active within the window.
func (t *Tracker) Alive(userID int64, iface string, within time.Duration) bool {
	if t == nil || iface == "" {
		return false
	}
	t.mu.Lock()
	last, ok := t.seen[key(userID, iface)]
	t.mu.Unlock()
	return ok && time.Since(last) <= within
}
