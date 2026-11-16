package api

import (
	"net/http"

	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/telegrambot"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// handleTelegramStatus tells a client whether to offer "Connect Telegram" and
// whether the caller already has a connected chat.
func (s *Server) handleTelegramStatus(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	st := types.TelegramStatus{Enabled: s.Telegram != nil}
	if st.Enabled {
		st.Linked = s.Telegram.Linked(r.Context(), u.ID)
	}
	writeJSON(w, http.StatusOK, st)
}

// handleTelegramLink issues a one-time t.me link that connects the Telegram
// chat opening it to the caller's account.
func (s *Server) handleTelegramLink(w http.ResponseWriter, r *http.Request) {
	if s.Telegram == nil {
		writeErr(w, http.StatusNotFound, "Telegram is not set up on this server")
		return
	}
	u, _ := auth.UserFromContext(r.Context())
	url, err := s.Telegram.NewLink(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, types.TelegramLinkResponse{URL: url, ExpiresIn: int(telegrambot.LinkTTL.Seconds())})
}

// handleTelegramUnlink disconnects the caller's Telegram chat.
func (s *Server) handleTelegramUnlink(w http.ResponseWriter, r *http.Request) {
	if s.Telegram == nil {
		writeErr(w, http.StatusNotFound, "Telegram is not set up on this server")
		return
	}
	u, _ := auth.UserFromContext(r.Context())
	if err := s.Telegram.Unlink(r.Context(), u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
