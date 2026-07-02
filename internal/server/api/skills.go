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

// errForbidden marks a scope write the caller lacks privilege for (shared).
var errForbidden = errors.New("forbidden")

// withSkillScopes runs fn with the caller's user DB and, when a valid ?project=
// is supplied and the caller is a member, that project's DB (else nil). This is
// the scope set skills resolve across (project → shared → user).
func (s *Server) withSkillScopes(r *http.Request, userID int64, fn func(userDB, projDB *sql.DB) error) error {
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	return s.Storage.WithUser(r.Context(), userID, func(udb *sql.DB) error {
		if projectID > 0 {
			if member, _ := s.Projects.IsMember(r.Context(), projectID, userID); member {
				return s.Storage.WithProject(r.Context(), projectID, func(pdb *sql.DB) error {
					return fn(udb, pdb)
				})
			}
		}
		return fn(udb, nil)
	})
}

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var infos []types.SkillInfo
	err := s.withSkillScopes(r, u.ID, func(udb, pdb *sql.DB) error {
		var e error
		infos, e = s.Skills.List(r.Context(), udb, pdb)
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
	var source string
	err := s.withSkillScopes(r, u.ID, func(udb, pdb *sql.DB) error {
		var e error
		files, source, e = s.Skills.ResolveRawFiles(r.Context(), udb, pdb, name)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "skill not found")
		return
	}
	writeJSON(w, http.StatusOK, types.SkillFiles{Name: name, Scope: source, Files: files})
}

// handlePutSkill writes a whole skill into the scope named in the body (or the
// default scope). Shared-scope writes require an elevated account.
func (s *Server) handlePutSkill(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	name := chi.URLParam(r, "name")
	var req types.SkillFiles
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	err := s.withSkillScopes(r, u.ID, func(udb, pdb *sql.DB) error {
		db, scope, e := s.Skills.ScopeDBFor(req.Scope, udb, pdb)
		if e != nil {
			return e
		}
		if scope == skills.ScopeShared && !types.IsElevated(u.Role) {
			return errForbidden
		}
		if e := s.Skills.Save(r.Context(), db, name, u.ID, req.Files); e != nil {
			return e
		}
		s.Audit.Log(r.Context(), udb, "skill_save", name+" ("+scope+")", nil)
		return nil
	})
	if err != nil {
		s.writeSkillErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	name := chi.URLParam(r, "name")
	scopeParam := r.URL.Query().Get("scope")
	if scopeParam == "" {
		// Deleting must never default to the project scope (unlike writes):
		// a bare "/skill reset" means "remove MY copy", not the project's.
		scopeParam = skills.ScopeUser
	}
	err := s.withSkillScopes(r, u.ID, func(udb, pdb *sql.DB) error {
		db, scope, e := s.Skills.ScopeDBFor(scopeParam, udb, pdb)
		if e != nil {
			return e
		}
		if scope == skills.ScopeShared && !types.IsElevated(u.Role) {
			return errForbidden
		}
		if e := s.Skills.Delete(r.Context(), db, name); e != nil {
			return e
		}
		s.Audit.Log(r.Context(), udb, "skill_delete", name+" ("+scope+")", nil)
		return nil
	})
	if err != nil {
		s.writeSkillErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleCreateSkill scaffolds a new empty skill in the default (or named) scope.
func (s *Server) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Scope       string `json:"scope"`
	}
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	err := s.withSkillScopes(r, u.ID, func(udb, pdb *sql.DB) error {
		db, scope, e := s.Skills.ScopeDBFor(req.Scope, udb, pdb)
		if e != nil {
			return e
		}
		if scope == skills.ScopeShared && !types.IsElevated(u.Role) {
			return errForbidden
		}
		if e := s.Skills.Create(r.Context(), db, req.Name, req.Description, u.ID); e != nil {
			return e
		}
		s.Audit.Log(r.Context(), udb, "skill_create", req.Name+" ("+scope+")", nil)
		return nil
	})
	if err != nil {
		s.writeSkillErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGetSkillFile returns one file of the effective skill.
func (s *Server) handleGetSkillFile(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	name := chi.URLParam(r, "name")
	relpath := chi.URLParam(r, "*")
	var content, source string
	err := s.withSkillScopes(r, u.ID, func(udb, pdb *sql.DB) error {
		var e error
		content, source, e = s.Skills.GetFile(r.Context(), udb, pdb, name, relpath)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "file not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "path": relpath, "scope": source, "content": content})
}

// handlePutSkillFile writes one file of a skill into a scope. If the scope does
// not yet hold the skill, the effective version is copied in first.
func (s *Server) handlePutSkillFile(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	name := chi.URLParam(r, "name")
	relpath := chi.URLParam(r, "*")
	var req struct {
		Scope   string `json:"scope"`
		Content string `json:"content"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	err := s.withSkillScopes(r, u.ID, func(udb, pdb *sql.DB) error {
		db, scope, e := s.Skills.ScopeDBFor(req.Scope, udb, pdb)
		if e != nil {
			return e
		}
		if scope == skills.ScopeShared && !types.IsElevated(u.Role) {
			return errForbidden
		}
		if e := s.Skills.PutFile(r.Context(), db, udb, pdb, name, relpath, req.Content, u.ID); e != nil {
			return e
		}
		s.Audit.Log(r.Context(), udb, "skill_file_save", name+"/"+relpath+" ("+scope+")", nil)
		return nil
	})
	if err != nil {
		s.writeSkillErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePublishSkill promotes the caller's effective version of a skill into the
// shared (org) scope.
func (s *Server) handlePublishSkill(w http.ResponseWriter, r *http.Request) {
	u, ok := requireElevated(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	err := s.withSkillScopes(r, u.ID, func(udb, pdb *sql.DB) error {
		files, _, e := s.Skills.ResolveRawFiles(r.Context(), udb, pdb, name)
		if e != nil {
			return e
		}
		shared, _, e := s.Skills.ScopeDBFor(skills.ScopeShared, udb, pdb)
		if e != nil {
			return e
		}
		if e := s.Skills.Save(r.Context(), shared, name, u.ID, files); e != nil {
			return e
		}
		s.Audit.Log(r.Context(), udb, "skill_publish", name, nil)
		return nil
	})
	if err != nil {
		s.writeSkillErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeSkillErr maps a skill operation error to an HTTP status.
func (s *Server) writeSkillErr(w http.ResponseWriter, err error) {
	if err == errForbidden {
		writeErr(w, http.StatusForbidden, "shared skills require an elevated account")
		return
	}
	writeErr(w, http.StatusBadRequest, err.Error())
}
