package api

import (
	"database/sql"
	"errors"
	"log"
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

// handleDepartProject removes the caller's own membership of a project (the
// "/project depart" command). Membership-only; it does not delete the project.
func (s *Server) handleDepartProject(w http.ResponseWriter, r *http.Request) {
	u, id, ok := s.projectMember(w, r)
	if !ok {
		return
	}
	if err := s.Projects.RemoveMember(r.Context(), id, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.dropProjectUser(id, u.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRemoveMember removes another member from a project. Allowed for the
// project's creator and for owners/admins (the same people who may delete it).
// The creator can't be removed (delete the project instead), and you remove
// yourself with depart.
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	uid, _ := strconv.ParseInt(chi.URLParam(r, "uid"), 10, 64)
	p, err := s.Projects.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	target, status, msg := checkRemoveMember(p, u, uid)
	if msg != "" {
		writeErr(w, status, msg)
		return
	}
	if err := s.Projects.RemoveMember(r.Context(), id, uid); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.dropProjectUser(id, uid)
	ctx := r.Context()
	_ = s.Storage.WithUser(ctx, u.ID, func(udb *sql.DB) error {
		s.Audit.Log(ctx, udb, "project_remove_member", p.Name+": "+target.Email, nil)
		return nil
	})
	if s.Notify != nil {
		_ = s.Storage.WithUser(ctx, uid, func(udb *sql.DB) error {
			_, _ = s.Notify.Create(ctx, udb, types.Notification{
				Title:       "Removed from project " + p.Name,
				Description: "Removed by " + u.Email + ". Its sessions, documents and memories are no longer available to you.",
			})
			return nil
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// checkRemoveMember decides whether actor may remove member uid from p,
// returning the member, or an HTTP status and message when refused.
func checkRemoveMember(p *types.Project, actor *types.User, uid int64) (*types.ProjectMember, int, string) {
	if p.CreatedBy != actor.ID && !types.IsElevated(actor.Role) {
		return nil, http.StatusForbidden, "only the project's creator or an owner/admin can remove members"
	}
	if uid == actor.ID {
		return nil, http.StatusBadRequest, "use depart to leave a project yourself"
	}
	if uid == p.CreatedBy {
		return nil, http.StatusBadRequest, "the project's creator can't be removed; delete the project instead"
	}
	for i := range p.Members {
		if p.Members[i].UserID == uid {
			return &p.Members[i], 0, ""
		}
	}
	return nil, http.StatusNotFound, "not a member of this project"
}

// dropProjectUser cuts a former member off the project's live session streams
// and team chat right away, instead of at their next reconnect.
func (s *Server) dropProjectUser(projectID, userID int64) {
	if s.Hub != nil {
		s.Hub.DropUser(projectID, userID)
	}
	if s.ChatHub != nil {
		s.ChatHub.DropUser(projectID, userID)
	}
}

// handleDeleteProject permanently deletes a project and all its data: registry
// rows, sessions, memories, documents, chat and uploaded files. Only the
// project's creator or an elevated role (owner/admin) may do it — an admin need
// not be a member. The caller must echo the exact project name in ?confirm= so a
// stray request can't destroy a workspace.
func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	p, err := s.Projects.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p.CreatedBy != u.ID && !types.IsElevated(u.Role) {
		writeErr(w, http.StatusForbidden, "only the project's creator or an admin can delete it")
		return
	}
	if r.URL.Query().Get("confirm") != p.Name {
		writeErr(w, http.StatusBadRequest, "confirmation required: pass the exact project name as ?confirm=")
		return
	}
	ctx := r.Context()
	// Registry first: with the memberships gone nothing can attach to the project
	// again, then drop the live connections before pulling the data away.
	if err := s.Projects.Delete(ctx, id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.Hub != nil {
		s.Hub.CloseProject(id)
	}
	if s.ChatHub != nil {
		s.ChatHub.CloseRoom(id)
	}
	if err := s.Storage.DeleteProjectData(id); err != nil {
		// The project is already unreachable; report the leftover loudly.
		log.Printf("projects: deleted project %d (%q) but removing its data failed: %v", id, p.Name, err)
		writeErr(w, http.StatusInternalServerError, "project deleted, but removing its files failed: "+err.Error())
		return
	}
	_ = s.Storage.WithUser(ctx, u.ID, func(udb *sql.DB) error {
		s.Audit.Log(ctx, udb, "project_delete", p.Name, nil)
		return nil
	})
	if s.Notify != nil {
		for _, m := range p.Members {
			if m.UserID == u.ID {
				continue
			}
			_ = s.Storage.WithUser(ctx, m.UserID, func(udb *sql.DB) error {
				_, _ = s.Notify.Create(ctx, udb, types.Notification{
					Title:       "Project deleted: " + p.Name,
					Description: "Deleted by " + u.Email + "; its sessions, documents and memories are gone.",
				})
				return nil
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
			n, e := s.Projects.MoveSessionToProject(r.Context(), udb, pdb, id, sid, u.ID)
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
