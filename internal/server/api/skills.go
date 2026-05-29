package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	infos, err := s.Skills.List(r.Context(), u.ID, u.Username)
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
	files, _, err := s.Skills.ResolveRawFiles(r.Context(), name, u.ID)
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
	if err := s.Skills.SaveOverride(r.Context(), name, u.ID, req.Files); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Audit.Log(r.Context(), &u.ID, "skill_override", name, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	name := chi.URLParam(r, "name")
	if err := s.Skills.DeleteOverride(r.Context(), name, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Audit.Log(r.Context(), &u.ID, "skill_reset", name, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handlePublishSkill(w http.ResponseWriter, r *http.Request) {
	u, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	// Publish the admin's effective version of the skill org-wide.
	files, _, err := s.Skills.ResolveRawFiles(r.Context(), name, u.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "skill not found")
		return
	}
	if err := s.Skills.Publish(r.Context(), name, u.ID, files); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Audit.Log(r.Context(), &u.ID, "skill_publish", name, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
