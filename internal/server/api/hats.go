package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// handleListHats returns the deployed hats (any authenticated user).
func (s *Server) handleListHats(w http.ResponseWriter, r *http.Request) {
	list, err := s.Skills.ListHats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []types.Hat{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handleGetHat returns one hat by name.
func (s *Server) handleGetHat(w http.ResponseWriter, r *http.Request) {
	h, err := s.Skills.GetHat(chi.URLParam(r, "name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "hat not found")
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// handleSetConversationHat sets (or clears) the hat worn by a conversation. The
// hat must exist unless it is being cleared.
func (s *Server) handleSetConversationHat(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req types.SetConversationHatRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Hat != "" {
		if _, err := s.Skills.GetHat(req.Hat); err != nil {
			if errors.Is(err, skills.ErrHatNotFound) {
				writeErr(w, http.StatusNotFound, "hat not found")
				return
			}
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if _, e := s.Conversations.Get(r.Context(), udb, id, u.ID); e != nil {
			return e
		}
		return s.Conversations.SetHat(r.Context(), udb, id, req.Hat)
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
