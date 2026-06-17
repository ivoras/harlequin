package api

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// presenceTouchInterval keeps a connected WebSocket client marked "live" for
// background tasks even during a long turn when it sends no frames.
const presenceTouchInterval = 30 * time.Second

// handleSessionWS upgrades to a WebSocket and attaches the connection to the
// session's live server-side goroutine. The turn runs under a session-scoped
// context (not this request), so a disconnect never cancels it: the client can
// reconnect later and resume. Streaming is WebSocket-only (no SSE).
//
// This route is outside the bearer-auth middleware group because browsers cannot
// set the Authorization header on a WebSocket; it authenticates here, accepting
// the token from either the Authorization header (Go clients) or the
// `bearer.<token>` WebSocket subprotocol (browsers).
func (s *Server) handleSessionWS(w http.ResponseWriter, r *http.Request) {
	user, err := s.Auth.UserForToken(r.Context(), wsToken(r))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)

	// Verify ownership before upgrading so we can return a clean JSON error.
	var sess *types.Session
	var ownErr error
	_ = s.Storage.WithUser(r.Context(), user.ID, func(udb *sql.DB) error {
		sess, ownErr = s.Sessions.Get(r.Context(), udb, id, user.ID)
		return nil
	})
	if ownErr != nil || sess == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{"harlequin"},
		// Auth is by bearer token (not cookies), so cross-site WebSocket hijacking
		// is not a concern: allow any origin (and the vite dev proxy).
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	defer c.CloseNow()

	// A connection-scoped context, cancelled when the client read loop ends
	// (close/drop) — independent of the session goroutine's lifetime.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := s.Hub.Attach(user, sess)
	defer sub.Close()

	if s.Presence != nil {
		s.Presence.Touch(user.ID, sess.Interface)
		go func() {
			t := time.NewTicker(presenceTouchInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					s.Presence.Touch(user.ID, sess.Interface)
				}
			}
		}()
	}

	// First frame is the resume handshake (hello); a client that opens without it
	// is treated as a cold resume (have_seq 0).
	var hello types.WSClientMessage
	if err := wsjson.Read(ctx, c, &hello); err != nil {
		return
	}

	// Read loop for subsequent client frames (prompt / interrupt).
	go func() {
		defer cancel()
		for {
			var m types.WSClientMessage
			if err := wsjson.Read(ctx, c, &m); err != nil {
				return
			}
			if s.Presence != nil {
				s.Presence.Touch(user.ID, sess.Interface)
			}
			switch m.Type {
			case types.WSClientPrompt:
				if strings.TrimSpace(m.Content) != "" {
					sub.Submit(m.Content)
				}
			case types.WSClientInterrupt:
				sub.Interrupt()
			}
		}
	}()

	// Writer: send the synced control frame, replay the buffered tail, then pump
	// live events. This goroutine is the connection's sole writer.
	synced, replay := sub.Resume(hello.HaveSeq)
	if err := wsjson.Write(ctx, c, synced); err != nil {
		return
	}
	for _, ev := range replay {
		if err := wsjson.Write(ctx, c, ev); err != nil {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events():
			if !ok {
				// Dropped as a slow consumer: ask the client to reconnect/cold-resume.
				_ = c.Close(websocket.StatusTryAgainLater, "resync")
				return
			}
			if err := wsjson.Write(ctx, c, ev); err != nil {
				return
			}
		}
	}
}

// wsToken extracts the bearer token for a WebSocket request from the Authorization
// header (Go clients) or, failing that, the `bearer.<token>` entry of the
// Sec-WebSocket-Protocol header (browsers, which cannot set Authorization).
func wsToken(r *http.Request) string {
	if t := bearerTokenHeader(r.Header.Get("Authorization")); t != "" {
		return t
	}
	for _, proto := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(proto, ",") {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "bearer.") {
				return strings.TrimPrefix(p, "bearer.")
			}
		}
	}
	return ""
}

func bearerTokenHeader(h string) string {
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[len("bearer "):])
	}
	return ""
}
