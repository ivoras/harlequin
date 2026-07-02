package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

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

// handleCreateHat scaffolds a new hat (elevated only).
func (s *Server) handleCreateHat(w http.ResponseWriter, r *http.Request) {
	u, ok := requireElevated(w, r)
	if !ok {
		return
	}
	var req types.CreateHatRequest
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.Skills.CreateHat(r.Context(), req.Name, req.Description, u.ID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		s.Audit.Log(r.Context(), udb, "hat_create", req.Name, nil)
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGetHatFiles returns a hat's raw files (any authenticated user).
func (s *Server) handleGetHatFiles(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	files, err := s.Skills.GetHatFiles(r.Context(), name)
	if err != nil {
		writeErr(w, http.StatusNotFound, "hat not found")
		return
	}
	writeJSON(w, http.StatusOK, types.HatFiles{Name: name, Files: files})
}

// handleGetHatFile returns one file of a hat.
func (s *Server) handleGetHatFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	relpath := chi.URLParam(r, "*")
	files, err := s.Skills.GetHatFiles(r.Context(), name)
	if err != nil {
		writeErr(w, http.StatusNotFound, "hat not found")
		return
	}
	content, ok := files[relpath]
	if !ok {
		writeErr(w, http.StatusNotFound, "file not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "path": relpath, "content": content})
}

// handlePutHatFile writes one file of a hat (elevated only).
func (s *Server) handlePutHatFile(w http.ResponseWriter, r *http.Request) {
	u, ok := requireElevated(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	relpath := chi.URLParam(r, "*")
	var req struct {
		Content string `json:"content"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.Skills.PutHatFile(r.Context(), name, relpath, req.Content, u.ID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		s.Audit.Log(r.Context(), udb, "hat_save", name+"/"+relpath, nil)
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAddHatSkill copies the currently-resolved skill into the hat's overlay
// (elevated only). The overlay copy then shadows normal resolution while worn.
func (s *Server) handleAddHatSkill(w http.ResponseWriter, r *http.Request) {
	u, ok := requireElevated(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	var req types.AddHatSkillRequest
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Skill) == "" {
		writeErr(w, http.StatusBadRequest, "skill is required")
		return
	}
	err := s.withSkillScopes(r, u.ID, func(udb, pdb *sql.DB) error {
		return s.Skills.AddHatSkill(r.Context(), udb, pdb, name, req.Skill, u.ID)
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		s.Audit.Log(r.Context(), udb, "hat_add_skill", name+"/"+req.Skill, nil)
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRemoveHatSkill drops a skill's overlay from the hat (elevated only).
func (s *Server) handleRemoveHatSkill(w http.ResponseWriter, r *http.Request) {
	u, ok := requireElevated(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	skill := chi.URLParam(r, "skill")
	if err := s.Skills.RemoveHatSkill(r.Context(), name, skill); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		s.Audit.Log(r.Context(), udb, "hat_remove_skill", name+"/"+skill, nil)
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
