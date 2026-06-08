package api

import (
	"database/sql"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var infos []types.SkillInfo
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		infos, e = s.Skills.List(r.Context(), udb, u.ID, u.Email)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if infos == nil {
		infos = []types.SkillInfo{}
	}
	writeJSON(w, http.StatusOK, infos)
}

func (s *Server) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	name := chi.URLParam(r, "name")
	var files map[string]string
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		files, _, e = s.Skills.ResolveRawFiles(r.Context(), udb, name, u.ID)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "skill not found")
		return
	}
	writeJSON(w, http.StatusOK, types.SkillFiles{Name: name, Files: files})
}

func (s *Server) handlePutSkill(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	name := chi.URLParam(r, "name")
	var req types.SkillFiles
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if e := s.Skills.SaveOverride(r.Context(), udb, name, u.ID, req.Files); e != nil {
			return e
		}
		s.Audit.Log(r.Context(), udb, "skill_override", name, nil)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	name := chi.URLParam(r, "name")
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if e := s.Skills.DeleteOverride(r.Context(), udb, name, u.ID); e != nil {
			return e
		}
		s.Audit.Log(r.Context(), udb, "skill_reset", name, nil)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handlePublishSkill(w http.ResponseWriter, r *http.Request) {
	u, ok := requireElevated(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	// Publish the admin's effective version of the skill org-wide.
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		files, _, e := s.Skills.ResolveRawFiles(r.Context(), udb, name, u.ID)
		if e != nil {
			return e
		}
		if e := s.Skills.Publish(r.Context(), name, u.ID, files); e != nil {
			return e
		}
		s.Audit.Log(r.Context(), udb, "skill_publish", name, nil)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
