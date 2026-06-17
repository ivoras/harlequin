// Package projectchat is a lightweight live broadcast hub for project chatrooms:
// one room per project, fanning posted messages out to every connected member.
// Unlike sessionhub there is no turn buffer or resume — clients load recent
// history from the project database on connect, then receive live messages.
package projectchat

import (
	"sync"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// subBuffer is the per-subscriber queue depth; a slow client that overflows is
// dropped (its channel closed) and reconnects.
const subBuffer = 64

// Hub holds the per-project rooms.
type Hub struct {
	mu    sync.Mutex
	rooms map[int64]map[*Subscriber]struct{}
}

// New constructs a Hub.
func New() *Hub { return &Hub{rooms: map[int64]map[*Subscriber]struct{}{}} }

// Subscriber is one connected member's live message queue.
type Subscriber struct {
	ch chan types.ChatMessage
}

// Events is the stream of broadcast messages for this connection.
func (s *Subscriber) Events() <-chan types.ChatMessage { return s.ch }

// Join registers a connection to a project's room.
func (h *Hub) Join(projectID int64) *Subscriber {
	sub := &Subscriber{ch: make(chan types.ChatMessage, subBuffer)}
	h.mu.Lock()
	room := h.rooms[projectID]
	if room == nil {
		room = map[*Subscriber]struct{}{}
		h.rooms[projectID] = room
	}
	room[sub] = struct{}{}
	h.mu.Unlock()
	return sub
}

// Leave removes a connection from a project's room.
func (h *Hub) Leave(projectID int64, sub *Subscriber) {
	h.mu.Lock()
	if room := h.rooms[projectID]; room != nil {
		if _, ok := room[sub]; ok {
			delete(room, sub)
			close(sub.ch)
		}
		if len(room) == 0 {
			delete(h.rooms, projectID)
		}
	}
	h.mu.Unlock()
}

// Broadcast fans a message out to every connection in the project's room. A
// subscriber whose queue is full is dropped (channel closed) so its writer ends
// and the client reconnects.
func (h *Hub) Broadcast(projectID int64, msg types.ChatMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	room := h.rooms[projectID]
	for sub := range room {
		select {
		case sub.ch <- msg:
		default:
			close(sub.ch)
			delete(room, sub)
		}
	}
	if len(room) == 0 {
		delete(h.rooms, projectID)
	}
}
