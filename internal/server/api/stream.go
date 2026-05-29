package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// handleSendMessage runs the agent loop and streams events as SSE. The request
// context cancellation (client Esc / disconnect) propagates to the agent loop.
func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if _, err := s.Conversations.Get(r.Context(), id, u.ID); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	var req types.SendMessageRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Serialize writes since the agent may emit from multiple goroutines.
	var mu sync.Mutex
	emit := func(ev types.StreamEvent) {
		mu.Lock()
		defer mu.Unlock()
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n\n"))
		flusher.Flush()
	}

	if err := s.Agent.Run(r.Context(), id, u.ID, u.Username, req.Content, emit); err != nil {
		// Best-effort error event (may already be sent by the loop).
		emit(types.StreamEvent{Type: types.SSEError, Error: err.Error()})
	}
}
