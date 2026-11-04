// Package docevents is an in-process pub/sub for "a document was created,
// updated or deleted", so connected clients can refresh their document lists
// without polling or a reload. Events are advisory: a subscriber that cannot keep
// up simply misses some, and clients answer every event by re-listing, so a
// dropped or coalesced event is harmless.
package docevents

import "sync"

// Operation kinds.
const (
	Created = "created"
	Updated = "updated"
	Deleted = "deleted"
	// SessionTitled is not a document change: a session got its auto-title, so
	// clients should re-list sessions (Scope/ProjectID/UserID say whose).
	SessionTitled = "session_titled"
)

// Event describes one document change.
type Event struct {
	Op        string
	Scope     string // personal | shared | project
	ProjectID int64  // for project scope
	UserID    int64  // owner, for personal scope
	DocID     int64
}

// Sub is one connection's subscription.
type Sub struct {
	C      chan Event
	userID int64
	member func(projectID int64) bool
}

// Bus fans events out to interested subscribers. The zero value is not usable;
// use New. All methods are safe on a nil *Bus (they do nothing), so callers need
// not guard.
type Bus struct {
	mu   sync.Mutex
	subs map[*Sub]struct{}
}

func New() *Bus { return &Bus{subs: map[*Sub]struct{}{}} }

// Subscribe registers a connection for userID. member reports whether the user
// belongs to a project (nil = none); it decides who sees project-scope events.
func (b *Bus) Subscribe(userID int64, member func(projectID int64) bool) *Sub {
	s := &Sub{C: make(chan Event, 32), userID: userID, member: member}
	if b == nil {
		return s
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Unsubscribe removes s.
func (b *Bus) Unsubscribe(s *Sub) {
	if b == nil || s == nil {
		return
	}
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
}

// Publish delivers ev to every subscriber allowed to see it: the owner for
// personal documents, everyone for shared ones, project members for project ones.
// It never blocks.
func (b *Bus) Publish(ev Event) {
	if b == nil {
		return
	}
	b.mu.Lock()
	targets := make([]*Sub, 0, len(b.subs))
	for s := range b.subs {
		targets = append(targets, s)
	}
	b.mu.Unlock()
	for _, s := range targets {
		if !s.visible(ev) {
			continue
		}
		select {
		case s.C <- ev:
		default: // full: the client will re-list on the events already queued
		}
	}
}

func (s *Sub) visible(ev Event) bool {
	switch ev.Scope {
	case "shared":
		return true
	case "project":
		return s.member != nil && s.member(ev.ProjectID)
	default: // personal
		return ev.UserID == s.userID
	}
}
