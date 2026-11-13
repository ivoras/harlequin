package api

import (
	"context"
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

// handleListHats returns the hats the caller can wear: shared ones plus their
// own personal ones, each labelled with its scope.
func (s *Server) handleListHats(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var list []types.Hat
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		list, e = s.Skills.ListHats(r.Context(), udb)
		return e
	})
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
	u, _ := auth.UserFromContext(r.Context())
	var h *types.Hat
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		h, e = s.Skills.GetHat(r.Context(), udb, chi.URLParam(r, "name"))
		return e
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "hat not found")
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// editHat runs fn against the named hat's home database. Shared hats need an
// elevated account; personal hats (the caller's own) do not. fn returns the
// audit action and target logged in the caller's db; a nil error answers ok.
// A status-carrying error from fn is passed through (see hatErr).
func (s *Server) editHat(w http.ResponseWriter, r *http.Request, fn func(u *types.User, udb, home *sql.DB, name string) (action, target string, err error)) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	name := chi.URLParam(r, "name")
	status, msg := http.StatusOK, ""
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		home, scope, err := s.Skills.HatHome(r.Context(), udb, name)
		if errors.Is(err, skills.ErrHatNotFound) {
			status, msg = http.StatusNotFound, "hat not found"
			return nil
		}
		if err != nil {
			return err
		}
		if scope == types.HatScopeShared && !types.IsElevated(u.Role) {
			status, msg = http.StatusForbidden, "shared hats can only be edited by owners and admins"
			return nil
		}
		action, target, err := fn(u, udb, home, name)
		if err != nil {
			status, msg = http.StatusBadRequest, err.Error()
			return nil
		}
		s.Audit.Log(r.Context(), udb, action, target, map[string]string{"scope": scope})
		return nil
	})
	switch {
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	case status != http.StatusOK:
		writeErr(w, status, msg)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// handlePutHat writes a whole hat (all its files): an existing one in its own
// scope, or — for a new name — a shared one (owners/admins; personal hats are
// made with POST /hats).
func (s *Server) handlePutHat(w http.ResponseWriter, r *http.Request) {
	var req types.HatFiles
	if err := decode(r, &req); err != nil || len(req.Files) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	u, _ := auth.UserFromContext(r.Context())
	name := chi.URLParam(r, "name")
	exists := false
	_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		_, _, err := s.Skills.HatHome(r.Context(), udb, name)
		exists = err == nil
		return nil
	})
	if !exists {
		if _, ok := requireElevated(w, r); !ok {
			return
		}
		if err := s.Skills.SaveHat(r.Context(), s.Storage.Shared, name, u.ID, req.Files); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			s.Audit.Log(r.Context(), udb, "hat_save", name, map[string]string{"scope": types.HatScopeShared})
			return nil
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	s.editHat(w, r, func(u *types.User, _, home *sql.DB, name string) (string, string, error) {
		return "hat_save", name, s.Skills.SaveHat(r.Context(), home, name, u.ID, req.Files)
	})
}

// handleDeleteHat removes a hat from its home database.
func (s *Server) handleDeleteHat(w http.ResponseWriter, r *http.Request) {
	s.editHat(w, r, func(_ *types.User, _, home *sql.DB, name string) (string, string, error) {
		return "hat_delete", name, s.Skills.DeleteHat(r.Context(), home, name)
	})
}

// handleCreateHat scaffolds a new hat. The default scope is personal; shared
// hats are for owners and admins (scope "shared").
func (s *Server) handleCreateHat(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req types.CreateHatRequest
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Scope == "" {
		req.Scope = types.HatScopePersonal
	}
	if req.Scope == types.HatScopeShared && !types.IsElevated(u.Role) {
		writeErr(w, http.StatusForbidden, "shared hats can only be created by owners and admins")
		return
	}
	var cerr error
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if cerr = s.Skills.CreateHat(r.Context(), udb, req.Scope, req.Name, req.Description, u.ID); cerr != nil {
			return nil
		}
		s.Audit.Log(r.Context(), udb, "hat_create", req.Name, map[string]string{"scope": req.Scope})
		return nil
	})
	switch {
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	case cerr != nil:
		writeErr(w, http.StatusBadRequest, cerr.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// hatFiles reads a hat's files wherever it lives (any authenticated user).
func (s *Server) hatFiles(r *http.Request, name string) (map[string]string, error) {
	u, _ := auth.UserFromContext(r.Context())
	var files map[string]string
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		home, _, err := s.Skills.HatHome(r.Context(), udb, name)
		if err != nil {
			return err
		}
		files, err = s.Skills.GetHatFiles(r.Context(), home, name)
		return err
	})
	return files, err
}

// handleGetHatFiles returns a hat's raw files (any authenticated user).
func (s *Server) handleGetHatFiles(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	files, err := s.hatFiles(r, name)
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
	files, err := s.hatFiles(r, name)
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

// handlePutHatFile writes one file of a hat.
func (s *Server) handlePutHatFile(w http.ResponseWriter, r *http.Request) {
	relpath := chi.URLParam(r, "*")
	var req struct {
		Content string `json:"content"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	s.editHat(w, r, func(u *types.User, _, home *sql.DB, name string) (string, string, error) {
		return "hat_save", name + "/" + relpath, s.Skills.PutHatFile(r.Context(), home, name, relpath, req.Content, u.ID)
	})
}

// handleAddHatSkill copies the currently-resolved skill into the hat's overlay.
// The overlay copy then shadows normal resolution while worn.
func (s *Server) handleAddHatSkill(w http.ResponseWriter, r *http.Request) {
	var req types.AddHatSkillRequest
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Skill) == "" {
		writeErr(w, http.StatusBadRequest, "skill is required")
		return
	}
	u, _ := auth.UserFromContext(r.Context())
	s.editHat(w, r, func(_ *types.User, udb, home *sql.DB, name string) (string, string, error) {
		// The project scope (?project=) is optional: it only decides which copy is overlaid.
		var pdb *sql.DB
		if pid, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64); pid > 0 {
			if member, _ := s.Projects.IsMember(r.Context(), pid, u.ID); member {
				return "hat_add_skill", name + "/" + req.Skill, s.Storage.WithProject(r.Context(), pid, func(p *sql.DB) error {
					return s.Skills.AddHatSkill(r.Context(), home, udb, p, name, req.Skill, u.ID)
				})
			}
		}
		return "hat_add_skill", name + "/" + req.Skill, s.Skills.AddHatSkill(r.Context(), home, udb, pdb, name, req.Skill, u.ID)
	})
}

// handleRemoveHatSkill drops a skill's overlay from the hat.
func (s *Server) handleRemoveHatSkill(w http.ResponseWriter, r *http.Request) {
	skill := chi.URLParam(r, "skill")
	s.editHat(w, r, func(_ *types.User, _, home *sql.DB, name string) (string, string, error) {
		return "hat_remove_skill", name + "/" + skill, s.Skills.RemoveHatSkill(r.Context(), home, name, skill)
	})
}

// handleSetHatPrompt enables/disables a hat's custom system prompt without
// losing its content.
func (s *Server) handleSetHatPrompt(w http.ResponseWriter, r *http.Request) {
	var req types.SetHatPromptRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	s.editHat(w, r, func(u *types.User, _, home *sql.DB, name string) (string, string, error) {
		return "hat_prompt", name + " enabled=" + strconv.FormatBool(req.Enabled), s.Skills.SetHatPromptEnabled(r.Context(), home, name, req.Enabled, u.ID)
	})
}

// handleGetSystemPromptTemplate returns the raw default system prompt template
// (elevated; used to seed a hat's custom prompt editor).
func (s *Server) handleGetSystemPromptTemplate(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireElevated(w, r); !ok {
		return
	}
	content, err := s.Skills.BakedSystemPrompt()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}

// handleSetSessionHat sets (or clears) the hat worn by a personal session.
func (s *Server) handleSetSessionHat(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	s.setSessionHat(w, r, u, 0, id)
}

// handleSetProjectSessionHat is the same for a project session (any member may).
func (s *Server) handleSetProjectSessionHat(w http.ResponseWriter, r *http.Request) {
	u, pid, ok := s.projectMember(w, r)
	if !ok {
		return
	}
	sid, _ := strconv.ParseInt(chi.URLParam(r, "sid"), 10, 64)
	s.setSessionHat(w, r, u, pid, sid)
}

// setSessionHat applies a hat to session id in the user's db (projectID 0) or
// the project's. The hat must exist unless it is being cleared.
func (s *Server) setSessionHat(w http.ResponseWriter, r *http.Request, u *types.User, projectID, id int64) {
	var req types.SetSessionHatRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := r.Context()
	apply := func(udb, sessDB *sql.DB) error {
		if _, e := s.Sessions.Get(ctx, sessDB, id, u.ID); e != nil {
			return e
		}
		return s.Agent.SetSessionHat(ctx, udb, sessDB, u.ID, projectID, id, req.Hat)
	}
	err := s.Storage.WithUser(ctx, u.ID, func(udb *sql.DB) error {
		if projectID == 0 {
			return apply(udb, udb)
		}
		return s.Storage.WithProject(ctx, projectID, func(pdb *sql.DB) error { return apply(udb, pdb) })
	})
	switch {
	case errors.Is(err, skills.ErrHatNotFound):
		writeErr(w, http.StatusNotFound, "hat not found")
	case errors.Is(err, skills.ErrPersonalHatInProject):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeErr(w, http.StatusNotFound, "not found")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// dropGoneHats blanks the hat of sessions whose hat no longer exists (deleted,
// or a removed baked-in one), so no client shows a chip for it.
func (s *Server) dropGoneHats(ctx context.Context, userDB *sql.DB, sessions []types.Session) {
	var live map[string]bool
	for i := range sessions {
		if sessions[i].Hat == nil || *sessions[i].Hat == "" {
			continue
		}
		if live == nil {
			live = map[string]bool{}
			hats, _ := s.Skills.ListHats(ctx, userDB)
			for _, h := range hats {
				live[h.Name] = true
			}
		}
		if !live[*sessions[i].Hat] {
			sessions[i].Hat = nil
		}
	}
}
