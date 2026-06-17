package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/project"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// projectMember authorizes the caller as a member of the {id} project, returning
// the user and project id. Any member has full project access (no sub-roles).
func (s *Server) projectMember(w http.ResponseWriter, r *http.Request) (*types.User, int64, bool) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return nil, 0, false
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	member, err := s.Projects.IsMember(r.Context(), id, u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, 0, false
	}
	if !member {
		writeErr(w, http.StatusForbidden, "not a project member")
		return nil, 0, false
	}
	return u, id, true
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req types.CreateProjectRequest
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	p, err := s.Projects.Create(r.Context(), strings.TrimSpace(req.Name), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	out, err := s.Projects.List(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []types.Project{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.projectMember(w, r)
	if !ok {
		return
	}
	p, err := s.Projects.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleInviteProject(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.projectMember(w, r)
	if !ok {
		return
	}
	var req types.InviteRequest
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Email) == "" {
		writeErr(w, http.StatusBadRequest, "email required")
		return
	}
	invitee, err := s.Projects.UserIDByEmail(r.Context(), strings.TrimSpace(req.Email))
	if errors.Is(err, project.ErrUserNotFound) {
		writeErr(w, http.StatusNotFound, "no account with that email")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Projects.Invite(r.Context(), id, invitee, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Notify the invitee (pushed to their connected clients via the session WS).
	if s.Notify != nil {
		if p, err := s.Projects.Get(r.Context(), id); err == nil {
			_ = s.Storage.WithUser(r.Context(), invitee, func(udb *sql.DB) error {
				_, _ = s.Notify.Create(r.Context(), udb, types.Notification{
					Title:       "Project invite: " + p.Name,
					Description: "Invited by " + u.Email + " — accept with /project invites",
				})
				return nil
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "invited"})
}

func (s *Server) handleListProjectInvites(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	out, err := s.Projects.ListInvites(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if out == nil {
		out = []types.ProjectInvite{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	inviteID, _ := strconv.ParseInt(chi.URLParam(r, "inviteID"), 10, 64)
	projectID, err := s.Projects.Accept(r.Context(), inviteID, u.ID)
	if errors.Is(err, project.ErrNoInvite) {
		writeErr(w, http.StatusNotFound, "invite not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"project_id": projectID})
}

// handleListProjectSessions lists the sessions assigned to the project (visible
// to any member).
func (s *Server) handleListProjectSessions(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.projectMember(w, r)
	if !ok {
		return
	}
	var out []types.Session
	err := s.Storage.WithProjectReadOnly(r.Context(), id, func(pdb *sql.DB) error {
		list, e := s.Sessions.List(r.Context(), pdb, u.ID, r.URL.Query().Get("q"))
		out = list
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	pid := id
	for i := range out {
		out[i].ProjectID = &pid
	}
	if out == nil {
		out = []types.Session{}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAssignSession moves the caller's personal session into the project.
func (s *Server) handleAssignSession(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.projectMember(w, r)
	if !ok {
		return
	}
	sid, _ := strconv.ParseInt(chi.URLParam(r, "sid"), 10, 64)
	var newID int64
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		return s.Storage.WithProject(r.Context(), id, func(pdb *sql.DB) error {
			n, e := s.Projects.MoveSessionToProject(r.Context(), udb, pdb, sid, u.ID)
			newID = n
			return e
		})
	})
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"session_id": newID})
}

// handleProjectMessages returns a project session's messages (member-only). The
// session id is the ?sid= query parameter.
func (s *Server) handleProjectMessages(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.projectMember(w, r)
	if !ok {
		return
	}
	sid, _ := strconv.ParseInt(r.URL.Query().Get("sid"), 10, 64)
	var out []types.Message
	err := s.Storage.WithProjectReadOnly(r.Context(), id, func(pdb *sql.DB) error {
		if _, e := s.Sessions.Get(r.Context(), pdb, sid, u.ID); e != nil {
			return e
		}
		msgs, e := s.Sessions.Messages(r.Context(), pdb, sid)
		out = msgs
		return e
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if out == nil {
		out = []types.Message{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeclineInvite(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	inviteID, _ := strconv.ParseInt(chi.URLParam(r, "inviteID"), 10, 64)
	if err := s.Projects.Decline(r.Context(), inviteID, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "declined"})
}
