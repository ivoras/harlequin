package api

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// handleProjectChatWS serves a project's live chatroom over a WebSocket: any
// member may connect. On connect it sends recent history, then streams messages
// posted by any member; clients post with a {"type":"chat","content":...} frame.
// Messages persist in the project database.
func (s *Server) handleProjectChatWS(w http.ResponseWriter, r *http.Request) {
	user, err := s.Auth.UserForToken(r.Context(), wsToken(r))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	projectID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if member, _ := s.Projects.IsMember(r.Context(), projectID, user.ID); !member {
		writeErr(w, http.StatusForbidden, "not a project member")
		return
	}

	// Recent history before upgrading isn't possible (we need the socket), so load
	// it after Accept and send it first.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:   []string{"harlequin"},
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	defer c.CloseNow()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := s.ChatHub.Join(projectID)
	defer s.ChatHub.Leave(projectID, sub)

	// Send recent history first.
	var history []types.ChatMessage
	_ = s.Storage.WithProjectReadOnly(r.Context(), projectID, func(pdb *sql.DB) error {
		h, e := s.Projects.ChatMessages(r.Context(), pdb, 100)
		history = h
		return e
	})
	for _, m := range history {
		msg := m
		if err := wsjson.Write(ctx, c, types.StreamEvent{Type: types.SSEChat, Chat: &msg}); err != nil {
			return
		}
	}

	// Reader: persist + broadcast posted messages.
	go func() {
		defer cancel()
		for {
			var m types.WSClientMessage
			if err := wsjson.Read(ctx, c, &m); err != nil {
				return
			}
			if m.Type != types.WSClientChat || strings.TrimSpace(m.Content) == "" {
				continue
			}
			var saved *types.ChatMessage
			_ = s.Storage.WithProject(ctx, projectID, func(pdb *sql.DB) error {
				cm, e := s.Projects.AddChatMessage(ctx, pdb, user.ID, strings.TrimSpace(m.Content))
				saved = cm
				return e
			})
			if saved != nil {
				s.ChatHub.Broadcast(projectID, *saved)
			}
		}
	}()

	// Writer: pump live messages (sole writer after the history send above).
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-sub.Events():
			if !ok {
				_ = c.Close(websocket.StatusTryAgainLater, "resync")
				return
			}
			msg := m
			if err := wsjson.Write(ctx, c, types.StreamEvent{Type: types.SSEChat, Chat: &msg}); err != nil {
				return
			}
		}
	}
}
