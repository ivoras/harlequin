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

// handleListHats returns the hats in the shared database (any authenticated user).
func (s *Server) handleListHats(w http.ResponseWriter, r *http.Request) {
	list, err := s.Skills.ListHats(r.Context())
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
	h, err := s.Skills.GetHat(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "hat not found")
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// handlePutHat writes a whole hat (all its files) into the shared database.
// Hats are org-wide, so this requires an elevated account.
func (s *Server) handlePutHat(w http.ResponseWriter, r *http.Request) {
	u, ok := requireElevated(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	var req types.HatFiles
	if err := decode(r, &req); err != nil || len(req.Files) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.Skills.SaveHat(r.Context(), name, u.ID, req.Files); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		s.Audit.Log(r.Context(), udb, "hat_save", name, nil)
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDeleteHat removes a hat from the shared database (elevated only).
func (s *Server) handleDeleteHat(w http.ResponseWriter, r *http.Request) {
	u, ok := requireElevated(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	if err := s.Skills.DeleteHat(r.Context(), name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		s.Audit.Log(r.Context(), udb, "hat_delete", name, nil)
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleSetSessionHat sets (or clears) the hat worn by a session. The
// hat must exist unless it is being cleared.
func (s *Server) handleSetSessionHat(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req types.SetSessionHatRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Hat != "" {
		if _, err := s.Skills.GetHat(r.Context(), req.Hat); err != nil {
			if errors.Is(err, skills.ErrHatNotFound) {
				writeErr(w, http.StatusNotFound, "hat not found")
				return
			}
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if _, e := s.Sessions.Get(r.Context(), udb, id, u.ID); e != nil {
			return e
		}
		return s.Sessions.SetHat(r.Context(), udb, id, req.Hat)
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
