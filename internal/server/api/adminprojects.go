package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/project"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Owner-only overview of every project, member or not (the normal list shows
// only the caller's own). Removing members reuses DELETE /projects/{id}/members/{uid}.

func (s *Server) handleAdminProjects(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireOwner(w, r); !ok {
		return
	}
	out, err := s.Projects.ListAll(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []types.Project{}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAdminJoinProject makes the owner a member of any project.
func (s *Server) handleAdminJoinProject(w http.ResponseWriter, r *http.Request) {
	me, ok := requireOwner(w, r)
	if !ok {
		return
	}
	p, ok := s.adminProject(w, r)
	if !ok {
		return
	}
	if err := s.Projects.AddMember(r.Context(), p.ID, me.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.auditOwner(r, "project_join", p.Name, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAdminAddMember adds a user by email directly (no invite round-trip).
func (s *Server) handleAdminAddMember(w http.ResponseWriter, r *http.Request) {
	me, ok := requireOwner(w, r)
	if !ok {
		return
	}
	p, ok := s.adminProject(w, r)
	if !ok {
		return
	}
	var req types.InviteRequest
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Email) == "" {
		writeErr(w, http.StatusBadRequest, "email required")
		return
	}
	uid, err := s.Projects.UserIDByEmail(r.Context(), strings.TrimSpace(req.Email))
	if errors.Is(err, project.ErrUserNotFound) {
		writeErr(w, http.StatusNotFound, "no account with that email")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Projects.AddMember(r.Context(), p.ID, uid); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.auditOwner(r, "project_add_member", p.Name+": "+strings.TrimSpace(req.Email), nil)
	if s.Notify != nil && uid != me.ID {
		_ = s.Storage.WithUser(r.Context(), uid, func(udb *sql.DB) error {
			_, _ = s.Notify.Create(r.Context(), udb, types.Notification{
				Title:       "Added to project " + p.Name,
				Description: "Added by " + me.Email + ".",
			})
			return nil
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) adminProject(w http.ResponseWriter, r *http.Request) (*types.Project, bool) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	p, err := s.Projects.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "project not found")
		return nil, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return p, true
}

// handleAdminProjectDocs lists a project's own documents (project corpus only).
// Files download through the normal /documents/{id}/file, which lets owners in.
func (s *Server) handleAdminProjectDocs(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireOwner(w, r); !ok {
		return
	}
	p, ok := s.adminProject(w, r)
	if !ok {
		return
	}
	var docs []types.Document
	err := s.Storage.WithProjectReadOnly(r.Context(), p.ID, func(pdb *sql.DB) error {
		var e error
		docs, e = s.Docs.ListScoped(r.Context(), []documents.ScopeDB{{Scope: documents.ScopeProject, DB: pdb}})
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if docs == nil {
		docs = []types.Document{}
	}
	writeJSON(w, http.StatusOK, docs)
}
