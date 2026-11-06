package api

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/ivoras/harlequin/internal/server/audit"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/docxextract"
	"github.com/ivoras/harlequin/internal/server/email"
	"github.com/ivoras/harlequin/internal/server/imgshrink"
	"github.com/ivoras/harlequin/internal/server/mdconv"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/session"
	"github.com/ivoras/harlequin/internal/server/sessionhub"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req types.LoginRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if s.limited(w, r, "login", req.Email, 10) {
		return
	}
	token, user, err := s.Auth.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.Storage.WithUser(r.Context(), user.ID, func(udb *sql.DB) error {
		s.Audit.Log(r.Context(), udb, "login", user.Email, nil)
		s.ensureOnboarding(r.Context(), udb, user.ID)
		return nil
	})
	writeJSON(w, http.StatusOK, types.LoginResponse{Token: token, User: *user})
}

// handleRegistrationStatus reports whether self-registration is enabled, so
// clients can show or hide the registration UI.
func (s *Server) handleRegistrationStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, types.RegistrationStatus{Enabled: s.Cfg.Auth.AllowRegistrationValue()})
}

// handleRegister starts self-registration: it validates the email, stores a
// pending registration, and emails (or logs) a verification magic code.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.Auth.AllowRegistrationValue() {
		writeErr(w, http.StatusForbidden, "registration is disabled")
		return
	}
	var req types.RegisterRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	addr := strings.ToLower(strings.TrimSpace(req.Email))
	if !email.ValidAddress(addr) {
		writeErr(w, http.StatusBadRequest, "invalid email address")
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if s.limited(w, r, "register", addr, 3) {
		return
	}
	code, err := s.Auth.StartRegistration(r.Context(), addr, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrCodeTooSoon) {
			writeErr(w, http.StatusTooManyRequests, "a code was just sent — wait a minute before requesting another")
			return
		}
		if errors.Is(err, auth.ErrUserExists) {
			writeErr(w, http.StatusConflict, "an account with that email already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	body := "Your Harlequin verification code is: " + code + "\n\nIt expires in 15 minutes."
	if err := s.Email.Send(addr, "Harlequin verification code", body); err != nil {
		// The client gets a generic message (this is an unauthenticated
		// endpoint); the actual SMTP failure goes to the server log.
		log.Printf("register: sending verification code to %s failed: %v", addr, err)
		writeErr(w, http.StatusInternalServerError, "failed to send verification code (server email problem — contact the administrator)")
		return
	}
	writeJSON(w, http.StatusOK, types.RegisterResponse{Status: "verification_sent", Email: addr})
}

// handleVerify completes registration with the emailed code, returning a login
// token on success (auto-login).
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.Auth.AllowRegistrationValue() {
		writeErr(w, http.StatusForbidden, "registration is disabled")
		return
	}
	var req types.VerifyRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	addr := strings.ToLower(strings.TrimSpace(req.Email))
	if s.limited(w, r, "verify", addr, 10) {
		return
	}
	token, user, err := s.Auth.VerifyRegistration(r.Context(), addr, strings.TrimSpace(req.Code))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrNoPendingRegistration):
			writeErr(w, http.StatusNotFound, "no pending registration for that email")
		case errors.Is(err, auth.ErrCodeExpired):
			writeErr(w, http.StatusGone, "verification code expired — please register again")
		case errors.Is(err, auth.ErrTooManyAttempts):
			writeErr(w, http.StatusTooManyRequests, "too many attempts — wait for the code to expire (15 minutes), then register again")
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeErr(w, http.StatusUnauthorized, "incorrect verification code")
		case errors.Is(err, auth.ErrUserExists):
			writeErr(w, http.StatusConflict, "an account with that email already exists")
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	_ = s.Storage.WithUser(r.Context(), user.ID, func(udb *sql.DB) error {
		s.Audit.Log(r.Context(), udb, "register", user.Email, nil)
		s.ensureOnboarding(r.Context(), udb, user.ID)
		return nil
	})
	writeJSON(w, http.StatusOK, types.LoginResponse{Token: token, User: *user})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	if len(token) > 7 {
		_ = s.Auth.Logout(r.Context(), token[7:])
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	writeJSON(w, http.StatusOK, u)
}

// handleChangeOwnPassword lets any user change their own password after
// proving the current one. All tokens are revoked (auth.ChangePassword), so
// the client must log in again.
func (s *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if s.limited(w, r, "login", u.Email, 10) {
		return
	}
	if len(req.New) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if _, _, err := s.Auth.Login(r.Context(), u.Email, req.Current); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeErr(w, http.StatusForbidden, "current password is incorrect")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Auth.ChangePassword(r.Context(), u.Email, req.New); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleOrg returns the configured organisation name for display in clients
// (public: it is shown on the login screen).
func (s *Server) handleOrg(w http.ResponseWriter, r *http.Request) {
	name := "Unnamed"
	if s.Cfg != nil && s.Cfg.Server.OrgName != "" {
		name = s.Cfg.Server.OrgName
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

// handleUserDirectory lists every account's id and email — deliberately only
// those two fields — for any authenticated user, so clients can offer
// autocomplete when inviting project members. Org members already see each
// other's emails inside shared projects; this widens nothing sensitive
// (roles, activity, and creation dates stay owner-only).
func (s *Server) handleUserDirectory(w http.ResponseWriter, r *http.Request) {
	if !s.Cfg.Auth.UserDirectoryValue() {
		writeErr(w, http.StatusForbidden, "the user directory is disabled on this server")
		return
	}
	users, err := s.Auth.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type entry struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
	}
	out := make([]entry, 0, len(users))
	for _, u := range users {
		out = append(out, entry{ID: u.ID, Email: u.Email})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireOwner(w, r); !ok {
		return
	}
	var req types.CreateUserRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Same rules as self-registration, so both paths yield loginable accounts.
	addr := strings.ToLower(strings.TrimSpace(req.Email))
	if !email.ValidAddress(addr) {
		writeErr(w, http.StatusBadRequest, "invalid email address")
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if req.Role != "" && !validRole(req.Role) {
		writeErr(w, http.StatusBadRequest, "invalid role (owner|admin|user)")
		return
	}
	if req.SendEmail && !s.Email.Configured() { // checked first: no account without its e-mail
		writeErr(w, http.StatusBadRequest, errNoSMTP)
		return
	}
	user, err := s.Auth.CreateUser(r.Context(), addr, req.Password, req.Role)
	if err != nil {
		if errors.Is(err, auth.ErrUserExists) {
			writeErr(w, http.StatusConflict, "an account with that email already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.auditOwner(r, "user_create", addr+" ("+user.Role+")", nil)
	out := userMailResult{User: user}
	if req.SendEmail {
		me, _ := auth.UserFromContext(r.Context())
		out.mail(s.Email.Send(addr, "Your "+s.productName()+" account",
			fmt.Sprintf("An account has been created for you on %s.\n\nSign in at: %s/\nEmail: %s\nPassword: %s\nRole: %s\n\nThis account was set up by %s. Keep this password private.\n",
				s.productName(), requestBaseURL(r), addr, req.Password, user.Role, me.Email)))
	}
	writeJSON(w, http.StatusCreated, out)
}

const errNoSMTP = "this server can't send e-mail (SMTP is not configured), so the password can't be e-mailed; pass it on yourself"

// userMailResult reports whether the sign-in e-mail went out; on failure the
// client keeps showing the password so the owner can pass it on.
type userMailResult struct {
	*types.User
	EmailSent  bool   `json:"email_sent"`
	EmailError string `json:"email_error,omitempty"`
}

func (u *userMailResult) mail(err error) {
	if err != nil {
		log.Printf("users: e-mailing sign-in details failed: %v", err) // never log the message itself
		u.EmailError = "the e-mail could not be sent: " + err.Error()
		return
	}
	u.EmailSent = true
}

// productName is "Harlequin (<org>)" when an organisation name is configured.
func (s *Server) productName() string {
	if n := strings.TrimSpace(s.Cfg.Server.OrgName); n != "" && n != "Unnamed" {
		return "Harlequin (" + n + ")"
	}
	return "Harlequin"
}

// auditOwner records a user-management action in the acting owner's audit log.
func (s *Server) auditOwner(r *http.Request, action, target string, detail any) {
	me, _ := auth.UserFromContext(r.Context())
	_ = s.Storage.WithUser(r.Context(), me.ID, func(udb *sql.DB) error {
		s.Audit.Log(r.Context(), udb, action, target, detail)
		return nil
	})
}

func validRole(r string) bool {
	return r == types.RoleOwner || r == types.RoleAdmin || r == types.RoleUser
}

// handleListUsers lists every account (owner only).
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireOwner(w, r); !ok {
		return
	}
	users, err := s.Auth.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if users == nil {
		users = []types.User{}
	}
	// can_email tells the UI whether "e-mail the password" is possible.
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "can_email": s.Email.Configured()})
}

// handleUpdateUser changes an account's role and/or password (owner only). A
// password change revokes the account's API tokens (signs it out everywhere).
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireOwner(w, r); !ok {
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req types.UpdateUserRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Role == "" && req.Password == "" {
		writeErr(w, http.StatusBadRequest, "nothing to change (role or password)")
		return
	}
	if req.Role != "" && !validRole(req.Role) {
		writeErr(w, http.StatusBadRequest, "invalid role (owner|admin|user)")
		return
	}
	if req.Password != "" && len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if req.SendEmail && (req.Password == "" || !s.Email.Configured()) {
		msg := errNoSMTP
		if req.Password == "" {
			msg = "send_email needs a new password"
		}
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	var out userMailResult
	if req.Role != "" {
		if err := s.Auth.SetRole(r.Context(), id, req.Role); err != nil {
			writeUserErr(w, err)
			return
		}
		s.auditOwner(r, "user_role", fmt.Sprintf("user %d -> %s", id, req.Role), nil)
	}
	if req.Password != "" {
		if err := s.Auth.SetPassword(r.Context(), id, req.Password); err != nil {
			writeUserErr(w, err)
			return
		}
		s.auditOwner(r, "user_password_reset", fmt.Sprintf("user %d", id), nil)
		if req.SendEmail {
			if addr, err := s.Auth.EmailByID(r.Context(), id); err != nil {
				out.mail(err)
			} else {
				me, _ := auth.UserFromContext(r.Context())
				out.mail(s.Email.Send(addr, "Your "+s.productName()+" password was reset",
					fmt.Sprintf("Your password on %s was reset by %s, and you have been signed out everywhere.\n\nSign in at: %s/\nEmail: %s\nNew password: %s\n\nKeep this password private.\n",
						s.productName(), me.Email, requestBaseURL(r), addr, req.Password)))
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteUser removes an account and its personal data directory (owner
// only). Owners can't delete themselves here, so a slip can't lock them out.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	me, ok := requireOwner(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if id == me.ID {
		writeErr(w, http.StatusBadRequest, "you can't delete your own account")
		return
	}
	var projects []types.Project // before the memberships go, to cut live streams
	if s.Projects != nil {
		projects, _ = s.Projects.List(r.Context(), id)
	}
	if err := s.Auth.DeleteUserByID(r.Context(), id); err != nil {
		writeUserErr(w, err)
		return
	}
	for _, p := range projects {
		s.dropProjectUser(p.ID, id)
	}
	s.auditOwner(r, "user_delete", fmt.Sprintf("user %d", id), nil)
	if err := os.RemoveAll(s.Storage.UserDir(id)); err != nil {
		log.Printf("users: deleted user %d but not its data directory: %v", id, err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeUserErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		writeErr(w, http.StatusNotFound, "user not found")
	case errors.Is(err, auth.ErrLastOwner):
		writeErr(w, http.StatusConflict, "there must be at least one owner: make someone else an owner first")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var sessions []types.Session
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		sessions, e = s.Sessions.List(r.Context(), udb, u.ID, r.URL.Query().Get("q"))
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var req types.CreateSessionRequest
	_ = decode(r, &req)
	var c *types.Session
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		// A plain "new session" reuses the last empty one instead of adding a blank.
		if req.Title == "" || req.Title == "New session" || req.Title == "Session" {
			if c, e = s.Sessions.ReuseEmpty(r.Context(), udb, u.ID, req.Hat, types.APIREST, reqInterface(r)); e != nil || c != nil {
				return e
			}
		}
		c, e = s.Sessions.Create(r.Context(), udb, u.ID, req.Title, req.Hat, types.APIREST, reqInterface(r))
		if e == nil { // reused empty sessions are not logged: nothing new was created
			s.Audit.Log(r.Context(), udb, "session_create", fmt.Sprintf("session %d", c.ID),
				map[string]string{"interface": reqInterface(r), "hat": req.Hat})
		}
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// handleSessionContext estimates how the session's next request would fill
// the model's context window, broken down by category (system prompt, skills,
// tools, messages). Pass ?project=<id> for a project session, and optionally
// ?model=<id> to size the context window against a specific model (typically
// the one last used, as reported by the client's most recent SSEDone event).
func (s *Server) handleSessionContext(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	model := r.URL.Query().Get("model")
	if projectID > 0 {
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
	} else {
		var ownErr error
		_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			_, ownErr = s.Sessions.Get(r.Context(), udb, id, u.ID)
			return nil
		})
		if ownErr != nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	breakdown, err := s.Agent.ContextBreakdown(r.Context(), projectID, id, u.ID, u.Email, u.Role, model)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	breakdown.Cost = s.sessionCost(r, projectID, id, u.ID)
	writeJSON(w, http.StatusOK, breakdown)
}

// sessionCost tallies a session's recorded LLM spend. Usage is recorded in the
// acting user's db, so a project session sums over every member's db. Best
// effort: nil (no cost shown) if anything fails.
func (s *Server) sessionCost(r *http.Request, projectID, sessionID, userID int64) *types.SessionCost {
	if s.Usage == nil {
		return nil
	}
	users := []int64{userID}
	if projectID > 0 {
		members, err := s.Projects.Members(r.Context(), projectID)
		if err != nil {
			return nil
		}
		users = users[:0]
		for _, m := range members {
			users = append(users, m.UserID)
		}
	}
	var acc types.SessionCost
	for _, uid := range users {
		if err := s.Storage.WithUser(r.Context(), uid, func(udb *sql.DB) error {
			return s.Usage.AddSessionCost(r.Context(), udb, projectID, sessionID, &acc)
		}); err != nil {
			log.Printf("usage: session cost (user %d, project %d, session %d): %v", uid, projectID, sessionID, err)
			return nil
		}
	}
	return &acc
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var msgs []types.Message
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if _, e := s.Sessions.Get(r.Context(), udb, id, u.ID); e != nil {
			return e
		}
		var e error
		msgs, e = s.Sessions.Messages(r.Context(), udb, id)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		return s.Sessions.Delete(r.Context(), udb, id, u.ID)
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRenameSession sets a user-chosen title on one of the caller's personal
// sessions (PATCH /sessions/{id} with {"title": "..."}).
func (s *Server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req struct {
		Title string `json:"title"`
	}
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Title) == "" {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if _, e := s.Sessions.Get(r.Context(), udb, id, u.ID); e != nil {
			return e
		}
		return s.Sessions.Rename(r.Context(), udb, id, strings.TrimSpace(req.Title))
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleClearSession wipes a session's messages so the next turn starts with a
// fresh context (the session, its title, and its hat survive). For a project
// session pass ?project=<id>; any member may clear it.
// handleRewindSession truncates a session back to before its n-th user message
// (body {"before_user": n, "expect": "<that message's start>"}), so the
// conversation continues from an earlier point. Refused while a turn runs.
func (s *Server) handleRewindSession(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	var req struct {
		BeforeUser int    `json:"before_user"`
		Expect     string `json:"expect"`
	}
	if err := decode(r, &req); err != nil || req.BeforeUser < 1 {
		writeErr(w, http.StatusBadRequest, "before_user (a 1-based user message index) is required")
		return
	}
	if s.Hub != nil && s.Hub.Busy(projectID, u.ID, id) {
		writeErr(w, http.StatusConflict, "a turn is running in this session — stop it (or wait) before rewinding")
		return
	}
	var removed string
	rewind := func(db *sql.DB) error {
		var e error
		removed, e = s.Sessions.RewindToUserMessage(r.Context(), db, id, req.BeforeUser, req.Expect)
		return e
	}
	var err error
	if projectID > 0 {
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err = s.Storage.WithProject(r.Context(), projectID, rewind)
	} else {
		err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			if _, e := s.Sessions.Get(r.Context(), udb, id, u.ID); e != nil {
				return e
			}
			return rewind(udb)
		})
	}
	switch {
	case errors.Is(err, session.ErrRewindMismatch):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	s.Session.Log(r.Context(), sessionlog.Event{
		SessionID: id, UserID: u.ID, Type: sessionlog.TypeSessionRewound,
		Data: map[string]any{"rewound_by": u.Email, "project_id": projectID, "before_user": req.BeforeUser},
	})
	writeJSON(w, http.StatusOK, map[string]string{"removed_prompt": removed})
}

func (s *Server) handleClearSession(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	var err error
	if projectID > 0 {
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err = s.Storage.WithProject(r.Context(), projectID, func(pdb *sql.DB) error {
			return s.Sessions.ClearMessages(r.Context(), pdb, id)
		})
	} else {
		err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			if _, e := s.Sessions.Get(r.Context(), udb, id, u.ID); e != nil {
				return e
			}
			return s.Sessions.ClearMessages(r.Context(), udb, id)
		})
	}
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	// Mark the clear in the trajectory log so a reader understands why the
	// composed context shrinks from here on.
	s.Session.Log(r.Context(), sessionlog.Event{
		SessionID: id, UserID: u.ID, Type: sessionlog.TypeSessionCleared,
		Data: map[string]any{"cleared_by": u.Email, "project_id": projectID},
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSessionLog(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	// Owners and admins may read the log.
	var ownErr error
	_ = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		_, ownErr = s.Sessions.Get(r.Context(), udb, id, u.ID)
		return nil
	})
	if ownErr != nil && !types.IsElevated(u.Role) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	data, err := s.Session.ReadAll(u.ID, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no log")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	_, _ = w.Write(data)
}

func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var mems []types.Memory
	// scope=project&project=<id>: the project's shared memories (members only).
	if r.URL.Query().Get("scope") == "project" {
		projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err := s.Storage.WithProjectReadOnly(r.Context(), projectID, func(pdb *sql.DB) error {
			var e error
			mems, e = s.Memory.ProjectList(r.Context(), pdb, 200)
			return e
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, mems)
		return
	}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		mems, e = s.Memory.List(r.Context(), udb, u.ID, r.URL.Query().Get("scope"), 200)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mems)
}

func (s *Server) handleListMemoryConflicts(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	conflicts := []types.MemoryConflict{}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		cs, e := s.Memory.ListConflicts(r.Context(), udb, u.ID, 200)
		if cs != nil {
			conflicts = cs
		}
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, conflicts)
}

func (s *Server) handleResolveMemoryConflict(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		return s.Memory.ResolveConflict(r.Context(), udb, id)
	})
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSearchMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	q := r.URL.Query().Get("q")
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	if projectID > 0 {
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
	}
	var res []types.SearchResult
	// When a project is active (?project=<id>), fuse project+shared+personal;
	// otherwise the requested scope (default personal+shared).
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if projectID > 0 {
			return s.Storage.WithProjectReadOnly(r.Context(), projectID, func(pdb *sql.DB) error {
				var e error
				res, e = s.Memory.SearchFused(r.Context(), udb, pdb, q, u.ID, 10)
				return e
			})
		}
		var e error
		res, e = s.Memory.Search(r.Context(), udb, q, u.ID, r.URL.Query().Get("scope"), 10)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleFindMemory returns full memory records matching a query (ranked
// best-first, across the user's own and shared memories) for listing.
func (s *Server) handleFindMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var mems []types.Memory
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		mems, e = s.Memory.Find(r.Context(), udb, r.URL.Query().Get("q"), u.ID, 25)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mems)
}

func (s *Server) handleGetMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")
	var m *types.Memory
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		m, e = s.Memory.Get(r.Context(), udb, id, u.ID)
		return e
	})
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleCreateMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var req types.CreateMemoryRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Scope == "shared" && !types.IsElevated(u.Role) {
		writeErr(w, http.StatusForbidden, "only owner or admin can create shared memories")
		return
	}
	if req.Scope == "project" {
		if member, _ := s.Projects.IsMember(r.Context(), req.ProjectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		var m *types.Memory
		err := s.Storage.WithProject(r.Context(), req.ProjectID, func(pdb *sql.DB) error {
			var e error
			m, e = s.Memory.ProjectAdd(r.Context(), pdb, req.Content, req.Source)
			return e
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, m)
		return
	}
	var m *types.Memory
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		// AddWithConflicts records any conflicts so they surface via
		// /memory/conflicts; the flagged hits themselves are not returned here.
		var e error
		m, _, e = s.Memory.AddWithConflicts(r.Context(), udb, req, u.ID)
		if e != nil {
			return e
		}
		s.Audit.Log(r.Context(), udb, "memory_write", req.Scope, nil)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handlePatchMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")
	var req types.UpdateMemoryRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}

	// p.N + project=<id>: a project memory (members only). Project memories
	// don't support slots (ProjectAdd never indexes any).
	if projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64); projectID > 0 && strings.HasPrefix(id, "p.") {
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		if req.Slots != nil {
			writeErr(w, http.StatusBadRequest, "project memories do not support slots")
			return
		}
		var m *types.Memory
		err := s.Storage.WithProject(r.Context(), projectID, func(pdb *sql.DB) error {
			if req.Content == nil {
				return nil
			}
			var e error
			m, e = s.Memory.ProjectChange(r.Context(), pdb, id, *req.Content)
			return e
		})
		if err != nil {
			if errors.Is(err, memory.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "not found")
				return
			}
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if m == nil {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		writeJSON(w, http.StatusOK, m)
		return
	}

	canManageShared := types.IsElevated(u.Role)
	var result *types.Memory
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if req.Pinned != nil {
			if e := s.Memory.SetPinned(r.Context(), udb, id, u.ID, *req.Pinned); e != nil {
				return e
			}
		}
		if req.Content != nil {
			if _, _, e := s.Memory.ChangeWithConflicts(r.Context(), udb, id, *req.Content, u.ID, canManageShared); e != nil {
				return e
			}
		}
		if req.Slots != nil {
			if e := s.Memory.SetSlots(r.Context(), udb, id, *req.Slots, canManageShared); e != nil {
				return e
			}
		}
		if req.Content != nil || req.Slots != nil {
			m, e := s.Memory.Get(r.Context(), udb, id, u.ID)
			if e != nil {
				return e
			}
			result = m
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if result != nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id := chi.URLParam(r, "id")
	// p.N + project=<id>: a project memory (members only).
	if projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64); projectID > 0 && strings.HasPrefix(id, "p.") {
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err := s.Storage.WithProject(r.Context(), projectID, func(pdb *sql.DB) error {
			return s.Memory.ProjectDelete(r.Context(), pdb, id)
		})
		if err != nil {
			if errors.Is(err, memory.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "not found")
				return
			}
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		if e := s.Memory.Delete(r.Context(), udb, id, u.ID, types.IsElevated(u.Role)); e != nil {
			return e
		}
		s.Audit.Log(r.Context(), udb, "memory_delete", id, nil)
		return nil
	})
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	var docs []types.Document
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		list := func(projDB *sql.DB) error {
			var e error
			docs, e = s.Docs.ListScoped(r.Context(), s.Docs.ScopesFor(udb, projDB))
			return e
		}
		if projectID > 0 {
			if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
				return list(nil) // not a member: fall back to personal+shared
			}
			return s.Storage.WithProjectReadOnly(r.Context(), projectID, list)
		}
		return list(nil)
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, docs)
}

// maxUploadBytes caps an uploaded document file (covers large PDFs, not media).
const maxUploadBytes = 64 << 20 // 64 MiB

func (s *Server) handleCreateDocument(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())

	// Same phase timeline as the async path (see handleIngestAsync): this route
	// runs the identical work, just inside the request, so a slow synchronous
	// upload has to be as diagnosable as a slow background one.
	pl := documents.NewPhaseLog("(upload)")
	defer pl.Done()
	ctx := documents.WithPhaseLog(r.Context(), pl)

	var req types.CreateDocumentRequest
	var raw []byte // raw uploaded bytes (nil for raw-text JSON ingests), persisted to disk
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		// File upload: extract text from the file (PDF via PDFium-wasm) and ingest.
		var err error
		req, raw, err = s.documentFromUpload(w, r)
		if err != nil {
			pl.Fail(err.Error())
			return // documentFromUpload already wrote the error
		}
	} else if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	pl.Relabel(cmp.Or(req.OriginalName, req.Title))
	pl.Phase("extract", fmt.Sprintf("%d chars, %d pages", len(req.Content), len(req.PageStarts)))

	// Catalogue description: one small LLM call over the opening text, stored
	// with the document so the assistant can resolve paraphrased references
	// ("the new EEA regulation") from list_documents. Best-effort.
	if req.Description == "" && s.Agent != nil {
		req.Description = s.Agent.DescribeDocument(ctx, req.Title, req.Content)
		pl.Phase("describe")
	}

	d, status, msg := s.ingestToScope(ctx, u, req, raw, nil)
	if msg != "" {
		pl.Fail(msg)
		writeErr(w, status, msg)
		return
	}
	s.afterIngest(u, req, d)
	writeJSON(w, http.StatusCreated, d)
}

// checkDocScope validates an ingest's target corpus and the caller's right to
// write there. A non-empty msg is a client-facing error with its status.
func (s *Server) checkDocScope(ctx context.Context, u *types.User, scope string, projectID int64) (int, string) {
	switch scope {
	case documents.ScopePersonal, "":
	case documents.ScopeShared:
		if !types.IsElevated(u.Role) {
			return http.StatusForbidden, "only owners/admins can add shared documents"
		}
	case documents.ScopeProject:
		if projectID == 0 {
			return http.StatusBadRequest, "project_id required for project scope"
		}
		if member, _ := s.Projects.IsMember(ctx, projectID, u.ID); !member {
			return http.StatusForbidden, "not a member of this project"
		}
	default:
		return http.StatusBadRequest, "invalid scope (personal|shared|project)"
	}
	return 0, ""
}

// ingestToScope routes an ingest to the requested corpus (with permission
// checks), persists the uploaded raw bytes under that scope's files/
// directory, and optionally reports embedding progress. A non-empty msg is a
// client-facing error with its status.
func (s *Server) ingestToScope(ctx context.Context, u *types.User, req types.CreateDocumentRequest, raw []byte, progress func(done, total int)) (*types.Document, int, string) {
	// ingestAndStore ingests into a corpus, then persists the uploaded file under
	// that scope's files/ directory with a 7-bit ASCII name (the original name is
	// kept in the DB) and records the on-disk path.
	ingestAndStore := func(ctx context.Context, db *sql.DB, filesDir string, dirErr error) (*types.Document, error) {
		doc, err := s.Docs.IngestIntoProgress(ctx, db, req, u.ID, progress)
		if err != nil || doc == nil || len(raw) == 0 || dirErr != nil || filesDir == "" {
			return doc, err
		}
		name := fmt.Sprintf("%d-%s", doc.ID, documents.AsciiName(req.OriginalName))
		if werr := os.WriteFile(filepath.Join(filesDir, name), raw, 0o600); werr != nil {
			log.Printf("documents: persist upload %q: %v", name, werr)
			return doc, err
		}
		if serr := s.Docs.SetStoredPath(ctx, db, doc.ID, name); serr == nil {
			doc.StoredPath = name
		}
		return doc, err
	}

	scope := req.Scope
	if scope == "" {
		scope = documents.ScopePersonal
	}
	if status, msg := s.checkDocScope(ctx, u, scope, req.ProjectID); msg != "" {
		return nil, status, msg
	}
	var d *types.Document
	var err error
	switch scope {
	case documents.ScopePersonal:
		err = s.Storage.WithUser(ctx, u.ID, func(udb *sql.DB) error {
			dir, dErr := s.Storage.UserFilesDir(u.ID)
			var e error
			d, e = ingestAndStore(ctx, udb, dir, dErr)
			return e
		})
	case documents.ScopeShared:
		dir, dErr := s.Storage.SharedFilesDir()
		d, err = ingestAndStore(ctx, s.Storage.Shared, dir, dErr)
	case documents.ScopeProject:
		err = s.Storage.WithProject(ctx, req.ProjectID, func(pdb *sql.DB) error {
			dir, dErr := s.Storage.ProjectFilesDir(req.ProjectID)
			var e error
			d, e = ingestAndStore(ctx, pdb, dir, dErr)
			return e
		})
	}
	if err != nil {
		return nil, http.StatusInternalServerError, err.Error()
	}
	if d != nil {
		d.Scope = scope // clients build "u.N"/"s.N"/"p<id>.N" references from it
		s.DocEvents.Publish(docevents.Event{Op: docevents.Created, Scope: scope, ProjectID: req.ProjectID, UserID: u.ID, DocID: d.ID})
	}
	return d, 0, ""
}

// afterIngest runs the post-ingest side work shared by the sync and async
// paths: the audit row, the optional document→memory bridge, and the
// background description retry.
func (s *Server) afterIngest(u *types.User, req types.CreateDocumentRequest, d *types.Document) {
	ctx := context.Background()
	_ = s.Storage.WithUser(ctx, u.ID, func(udb *sql.DB) error {
		s.Audit.Log(ctx, udb, "document_ingest", req.Title, nil)
		return nil
	})
	// Optional bridge: distill durable facts from the document into memory.
	if s.Cfg.Memory.ExtractFromDocumentsEnabled() && s.Agent != nil && strings.TrimSpace(req.Content) != "" {
		go s.Agent.ExtractMemoriesFromText(context.Background(), u.ID, req.Title, req.Content, types.IsElevated(u.Role))
	}
	// The synchronous description attempt can lose to model load/contention;
	// retry in the background and repair the stored row.
	scope := req.Scope
	if scope == "" {
		scope = documents.ScopePersonal
	}
	if d != nil && req.Description == "" && s.Agent != nil {
		s.describeLater(u.ID, scope, req.ProjectID, d.ID, req.Title, req.Content)
	}
}

// handleCreateDocumentAsync accepts a multipart document upload, starts the
// conversion+ingest in the background, and returns a job id immediately; the
// client polls GET /documents/jobs/{id} for stage/progress. Scope permission
// is checked up front so a forbidden upload fails synchronously.
func (s *Server) handleCreateDocumentAsync(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	in, err := readUploadInput(w, r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	scope := in.scope
	if scope == "" {
		scope = documents.ScopePersonal
	}
	if status, msg := s.checkDocScope(r.Context(), u, scope, in.projectID); msg != "" {
		writeErr(w, status, msg)
		return
	}
	job := s.ingest.start(u.ID)
	uCopy := *u
	go func() {
		// Independent lifetime: the upload request returns immediately.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		// Per-phase timing: an ingestion that takes many minutes is the sum of a
		// converter call, one LLM call and two embedding passes, and without this
		// there is no way to tell afterwards which of them was slow. The log
		// travels on ctx so the chunk/embed phases inside the documents package
		// join the same timeline.
		pl := documents.NewPhaseLog(in.filename)
		defer pl.Done()
		ctx = documents.WithPhaseLog(ctx, pl)

		job.set("extracting")
		req, _, msg := s.extractDocument(ctx, in)
		if msg != "" {
			pl.Fail(msg)
			job.fail(msg)
			return
		}
		pl.Phase("extract", fmt.Sprintf("%d chars, %d pages", len(req.Content), len(req.PageStarts)))
		if req.Description == "" && s.Agent != nil {
			job.set("describing")
			req.Description = s.Agent.DescribeDocument(ctx, req.Title, req.Content)
			pl.Phase("describe")
		}
		// The progress callback flips this to "embedding" at the first batch.
		job.set("chunking")
		d, _, msg := s.ingestToScope(ctx, &uCopy, req, in.data, job.progress)
		if msg != "" {
			pl.Fail(msg)
			job.fail(msg)
			return
		}
		s.afterIngest(&uCopy, req, d)
		job.succeed(d)
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.id})
}

// handleGetIngestJob reports an async ingestion's stage/progress (creator only).
func (s *Server) handleGetIngestJob(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	j := s.ingest.get(chi.URLParam(r, "id"), u.ID)
	if j == nil {
		writeErr(w, http.StatusNotFound, "no such ingest job (it may have expired or the server restarted)")
		return
	}
	writeJSON(w, http.StatusOK, j.status())
}

// describeLater regenerates a document's missing catalogue description in the
// background and stores it. It runs behind the background-LLM gate, so it waits
// out the post-upload memory-bridge job and any live turns instead of timing
// out against them.
func (s *Server) describeLater(userID int64, scope string, projectID, docID int64, title, content string) {
	go func() {
		ctx := context.Background()
		desc := s.Agent.DescribeDocumentBackground(ctx, title, content)
		if desc == "" {
			return
		}
		var err error
		switch scope {
		case documents.ScopeShared:
			err = s.Docs.SetDescription(ctx, s.Storage.Shared, docID, desc)
		case documents.ScopeProject:
			err = s.Storage.WithProject(ctx, projectID, func(pdb *sql.DB) error {
				return s.Docs.SetDescription(ctx, pdb, docID, desc)
			})
		default:
			err = s.Storage.WithUser(ctx, userID, func(udb *sql.DB) error {
				return s.Docs.SetDescription(ctx, udb, docID, desc)
			})
		}
		if err != nil {
			log.Printf("documents: store retried description for %q: %v", title, err)
			return
		}
		log.Printf("documents: described %q on background retry", title)
	}()
}

// documentFromUpload reads a multipart "file" field, extracts its text (PDF and
// DOCX go through Docling when configured, else PDFium-wasm / docxextract;
// text-like files are used as-is), and builds an ingest request. On failure it
// writes the HTTP error and returns a non-nil error.
// It also returns the raw uploaded bytes so the caller can persist the file.
// uploadInput is the quickly-read multipart payload of a document upload; the
// slow text extraction happens separately (extractDocument), so the async path
// can return a job id before conversion starts.
type uploadInput struct {
	data        []byte
	filename    string
	contentType string // the file part's declared Content-Type
	title       string
	scope       string
	projectID   int64
}

// readUploadInput reads the multipart form (file bytes + fields) without any
// conversion work.
func readUploadInput(w http.ResponseWriter, r *http.Request) (uploadInput, error) {
	var in uploadInput
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		return in, fmt.Errorf("invalid upload: %w", err)
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return in, fmt.Errorf("missing file field")
	}
	defer file.Close()
	in.data, err = io.ReadAll(file)
	if err != nil {
		return in, fmt.Errorf("read file: %w", err)
	}
	in.filename = header.Filename
	in.contentType = header.Header.Get("Content-Type")
	in.title = r.FormValue("title")
	if in.title == "" {
		in.title = header.Filename
	}
	// Oversized images are re-encoded (same pixels, smaller file) so what is
	// stored and sent to vision models stays within maxImageUploadBytes.
	if imageMime(in.filename) != "" {
		if in.data, in.filename, err = imgshrink.Shrink(in.data, in.filename, maxImageUploadBytes); err != nil {
			return in, fmt.Errorf("image %q: %w", header.Filename, err)
		}
		in.contentType = imageMime(in.filename)
	}
	in.scope = r.FormValue("scope")
	in.projectID, _ = strconv.ParseInt(r.FormValue("project_id"), 10, 64)
	return in, nil
}

func (s *Server) documentFromUpload(w http.ResponseWriter, r *http.Request) (types.CreateDocumentRequest, []byte, error) {
	var req types.CreateDocumentRequest
	in, err := readUploadInput(w, r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return req, nil, err
	}
	req, status, msg := s.extractDocument(r.Context(), in)
	if msg != "" {
		writeErr(w, status, msg)
		return req, nil, fmt.Errorf("%s", msg)
	}
	return req, in.data, nil
}

// extractDocument converts an upload's bytes to an ingest request (Docling /
// PDFium-wasm / docxextract / raw text). A non-empty msg is a client-facing
// error with its HTTP status.
func (s *Server) extractDocument(ctx context.Context, in uploadInput) (req types.CreateDocumentRequest, status int, msg string) {
	data, title, scope, projectID := in.data, in.title, in.scope, in.projectID
	header := struct{ Filename string }{in.filename}

	const docxMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	lowName := strings.ToLower(header.Filename)
	isPDF := strings.HasSuffix(lowName, ".pdf") ||
		in.contentType == "application/pdf" ||
		bytes.HasPrefix(data, []byte("%PDF-"))
	isDOCX := !isPDF && (strings.HasSuffix(lowName, ".docx") ||
		in.contentType == docxMime)

	var pageStarts []int
	switch {
	case isPDF, isDOCX:
		mime := "application/pdf"
		if isDOCX {
			mime = docxMime
		}
		// The configured converter (docling or xberg) first: structured text
		// (layout, tables, OCR). PDFs go through its paged path so page citations
		// survive; DOCX (unpaged) takes the plain Markdown one. Any failure falls
		// back to the built-in extractors below.
		var text string
		if s.Converter != nil {
			name := s.Converter.Name()
			var md string
			var err error
			if isPDF {
				md, pageStarts, err = s.Converter.ConvertPaged(ctx, header.Filename, data)
			} else {
				md, err = s.Converter.Convert(ctx, header.Filename, data)
			}
			if err != nil {
				pageStarts = nil
				log.Printf("documents: %s convert %q failed, using built-in extractor: %v", name, header.Filename, err)
			} else {
				log.Printf("documents: %s converted %q (%d chars, %d pages)", name, header.Filename, len(md), len(pageStarts))
				text = md
			}
		}
		switch {
		case text != "":
			// Extracted above (PDFs keep their page mapping via pageStarts).
		case isPDF:
			if s.PDFExtract == nil {
				return req, http.StatusUnsupportedMediaType, "PDF extraction is not available on this server"
			}
			pages, err := s.PDFExtract.Pages(data)
			if err != nil {
				return req, http.StatusUnprocessableEntity, "PDF extraction failed: " + err.Error()
			}
			// Join pages (matching Text's "\n\n" separator) and record the rune
			// offset at which each page begins so chunks can be mapped to a page.
			var sb strings.Builder
			runeOff := 0
			for i, pg := range pages {
				if i > 0 {
					sb.WriteString("\n\n")
					runeOff += 2
				}
				pageStarts = append(pageStarts, runeOff)
				sb.WriteString(pg)
				runeOff += utf8.RuneCountInString(pg)
			}
			text = sb.String()
		default: // DOCX
			t, err := docxextract.Text(data)
			if err != nil {
				return req, http.StatusUnprocessableEntity, "DOCX extraction failed: " + err.Error()
			}
			text = t
		}
		if strings.TrimSpace(text) == "" {
			return req, http.StatusUnprocessableEntity, "no extractable text in the file (it may be scanned images)"
		}
		log.Printf("documents: extracted %d chars from %q for user", len(text), header.Filename)
		req = types.CreateDocumentRequest{Title: title, URI: "upload://" + header.Filename, Mime: mime, Content: text}
	case imageMime(header.Filename) != "":
		// Images are kept as files and shown to vision models inline (see
		// agent.attachImages). Their searchable text is OCR output when a
		// converter is configured, else just the filename.
		text := ""
		if s.Converter != nil {
			if md, err := s.Converter.Convert(ctx, header.Filename, data); err == nil {
				text = strings.TrimSpace(md)
			}
		}
		if text == "" {
			text = "Image: " + header.Filename
		}
		req = types.CreateDocumentRequest{Title: title, URI: "upload://" + header.Filename, Mime: imageMime(header.Filename), Content: text}
	case mdconv.Mime(header.Filename) != "":
		// HTML/PPTX/XLSX/ODT/EPUB/RTF -> Markdown via the configured converter
		// (docling/xberg: better layout and tables); the pure-Go mdconv takes
		// over when none is configured or it fails/rejects the format.
		var md string
		if s.Converter != nil {
			var err error
			if md, err = s.Converter.Convert(ctx, header.Filename, data); err != nil {
				log.Printf("documents: %s convert %q failed, using built-in converter: %v", s.Converter.Name(), header.Filename, err)
				md = ""
			}
		}
		if strings.TrimSpace(md) == "" {
			var err error
			if md, err = mdconv.Convert(header.Filename, data); err != nil {
				return req, http.StatusUnprocessableEntity, "conversion failed: " + err.Error()
			}
		}
		req = types.CreateDocumentRequest{Title: title, URI: "upload://" + header.Filename, Mime: mdconv.Mime(header.Filename), Content: md}
	case utf8.Valid(data):
		req = types.CreateDocumentRequest{Title: title, URI: "upload://" + header.Filename, Mime: "text/plain", Content: string(data)}
	default:
		return req, http.StatusUnsupportedMediaType, "unsupported file type (only PDF, DOCX and text are supported)"
	}
	req.Scope = scope
	req.ProjectID = projectID
	req.OriginalName = header.Filename
	req.PageStarts = pageStarts
	return req, 0, ""
}

func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = documents.ScopeShared // back-compatible default
	}
	var err error
	switch scope {
	case documents.ScopePersonal:
		filesDir, _ := s.Storage.UserFilesDir(u.ID)
		err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			return s.Docs.DeleteFrom(r.Context(), udb, filesDir, id)
		})
	case documents.ScopeShared:
		if _, ok := requireElevated(w, r); !ok {
			return
		}
		filesDir, _ := s.Storage.SharedFilesDir()
		err = s.Docs.DeleteFrom(r.Context(), s.Docs.SharedDB(), filesDir, id)
	case documents.ScopeProject:
		projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		filesDir, _ := s.Storage.ProjectFilesDir(projectID)
		err = s.Storage.WithProject(r.Context(), projectID, func(pdb *sql.DB) error {
			return s.Docs.DeleteFrom(r.Context(), pdb, filesDir, id)
		})
	default:
		writeErr(w, http.StatusBadRequest, "invalid scope (personal|shared|project)")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	pid, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	s.DocEvents.Publish(docevents.Event{Op: docevents.Deleted, Scope: scope, ProjectID: pid, UserID: u.ID, DocID: id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGetDocChunk resolves a chunk citation id (d.u.N / d.s.N / d.p.N) to
// its document, title, page, and file availability, so clients can render
// citations as links. Project chunks need ?project=<id> (member only).
func (s *Server) handleGetDocChunk(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	cid := chi.URLParam(r, "cid")
	parts := strings.Split(cid, ".")
	if len(parts) != 3 || parts[0] != "d" {
		writeErr(w, http.StatusBadRequest, "invalid chunk id (want d.u.N / d.s.N / d.p.N / d.p<project>.N)")
		return
	}
	local, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid chunk id")
		return
	}
	var info *types.DocChunkInfo
	scopeToken := parts[1]
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	if len(scopeToken) > 1 && scopeToken[0] == 'p' {
		// Qualified project citation (d.p<id>.N): the project id rides in the
		// reference itself, overriding the session's project parameter.
		if pid, err := strconv.ParseInt(scopeToken[1:], 10, 64); err == nil && pid > 0 {
			projectID = pid
			scopeToken = "p"
		}
	}
	switch scopeToken {
	case "u":
		err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			var e error
			info, e = s.Docs.ChunkInfo(r.Context(), udb, local)
			return e
		})
		if info != nil {
			info.Scope = documents.ScopePersonal
		}
	case "s":
		info, err = s.Docs.ChunkInfo(r.Context(), s.Docs.SharedDB(), local)
		if info != nil {
			info.Scope = documents.ScopeShared
		}
	case "p":
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err = s.Storage.WithProjectReadOnly(r.Context(), projectID, func(pdb *sql.DB) error {
			var e error
			info, e = s.Docs.ChunkInfo(r.Context(), pdb, local)
			return e
		})
		if info != nil {
			info.Scope = documents.ScopeProject
			info.ProjectID = projectID
		}
	default:
		writeErr(w, http.StatusBadRequest, "invalid chunk scope")
		return
	}
	if err != nil || info == nil {
		writeErr(w, http.StatusNotFound, "chunk not found")
		return
	}
	info.ID = cid
	writeJSON(w, http.StatusOK, info)
}

// handleGetDocumentFile serves a document's stored original file (inline, with
// its mime type) so citations can open it — e.g. a PDF at #page=N. Scope rules
// mirror the corpora: personal = the caller's own, shared = any authenticated
// user, project = members.
func (s *Server) handleGetDocumentFile(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	scope := r.URL.Query().Get("scope")
	var dir, stored, mime, title string
	var err error
	switch scope {
	case documents.ScopePersonal:
		err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			var e error
			stored, mime, title, e = s.Docs.StoredFile(r.Context(), udb, id)
			return e
		})
		if err == nil {
			dir, err = s.Storage.UserFilesDir(u.ID)
		}
	case documents.ScopeShared, "":
		stored, mime, title, err = s.Docs.StoredFile(r.Context(), s.Docs.SharedDB(), id)
		if err == nil {
			dir, err = s.Storage.SharedFilesDir()
		}
	case documents.ScopeProject:
		projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err = s.Storage.WithProjectReadOnly(r.Context(), projectID, func(pdb *sql.DB) error {
			var e error
			stored, mime, title, e = s.Docs.StoredFile(r.Context(), pdb, id)
			return e
		})
		if err == nil {
			dir, err = s.Storage.ProjectFilesDir(projectID)
		}
	default:
		writeErr(w, http.StatusBadRequest, "invalid scope")
		return
	}
	if err != nil || stored == "" || !filepath.IsLocal(stored) {
		writeErr(w, http.StatusNotFound, "no stored file for this document")
		return
	}
	if mime != "" {
		// Stored text is always UTF-8; without an explicit charset a browser
		// viewing the served file (e.g. via a blob: URL) falls back to
		// windows-1252 and mangles anything beyond ASCII.
		if strings.HasPrefix(mime, "text/") {
			mime += "; charset=utf-8"
		}
		w.Header().Set("Content-Type", mime)
	}
	w.Header().Set("Content-Disposition", "inline; filename=\""+documents.AsciiName(title)+"\"")
	http.ServeFile(w, r, filepath.Join(dir, stored))
}

// handleGetDocumentContent returns a document's full text content — every
// chunk concatenated in order — for documents with no stored original file
// (plain-text ingests, including save_doc reports). PDF/DOCX uploads should
// use /documents/{id}/file instead (their content lives in an actual file);
// this endpoint works for any document type but exists specifically to view
// TXT-type documents that /file cannot serve.
func (s *Server) handleGetDocumentContent(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	scope := r.URL.Query().Get("scope")
	// FullText must run inside the WithUser/WithProjectReadOnly callback: both
	// close their *sql.DB when the callback returns, so capturing the handle
	// for use afterward (as an earlier version of this handler did) queries an
	// already-closed connection and every lookup fails as "not found".
	var content string
	var err error
	switch scope {
	case documents.ScopePersonal:
		err = s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
			var e error
			content, e = s.Docs.FullText(r.Context(), udb, id)
			return e
		})
	case documents.ScopeShared, "":
		content, err = s.Docs.FullText(r.Context(), s.Docs.SharedDB(), id)
	case documents.ScopeProject:
		projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
		err = s.Storage.WithProjectReadOnly(r.Context(), projectID, func(pdb *sql.DB) error {
			var e error
			content, e = s.Docs.FullText(r.Context(), pdb, id)
			return e
		})
	default:
		writeErr(w, http.StatusBadRequest, "invalid scope")
		return
	}
	if err != nil || content == "" {
		writeErr(w, http.StatusNotFound, "document not found or has no content")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}

func (s *Server) handleSearchDocuments(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	q := r.URL.Query().Get("q")
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)
	if projectID > 0 {
		if member, _ := s.Projects.IsMember(r.Context(), projectID, u.ID); !member {
			writeErr(w, http.StatusForbidden, "not a member of this project")
			return
		}
	}
	var res []types.SearchResult
	// Fuse the user's personal corpus and (when ?project=<id> is set, i.e. the
	// user has switched to a project) the project corpus with the shared corpus.
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		search := func(projDB *sql.DB) error {
			var e error
			res, e = s.Docs.SearchScoped(r.Context(), s.Docs.ScopesFor(udb, projDB), q, 10)
			return e
		}
		if projectID > 0 {
			return s.Storage.WithProjectReadOnly(r.Context(), projectID, search)
		}
		return search(nil)
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// usageTarget is whose usage a request reads: your own, or (owners/admins)
// another user's via ?user_id=.
func usageTarget(r *http.Request) int64 {
	u, _ := auth.UserFromContext(r.Context())
	if q := r.URL.Query().Get("user_id"); q != "" && types.IsElevated(u.Role) {
		if id, err := strconv.ParseInt(q, 10, 64); err == nil {
			return id
		}
	}
	return u.ID
}

// handleUsageDaily returns per-day usage totals over all records; ?tz= is the
// viewer's UTC offset in minutes (e.g. 60) so days break at local midnight.
func (s *Server) handleUsageDaily(w http.ResponseWriter, r *http.Request) {
	target := usageTarget(r)
	tz, _ := strconv.Atoi(r.URL.Query().Get("tz"))
	if tz < -14*60 || tz > 14*60 {
		writeErr(w, http.StatusBadRequest, "tz must be a UTC offset in minutes (-840..840)")
		return
	}
	var days []types.UsageDay
	err := s.Storage.WithUser(r.Context(), target, func(udb *sql.DB) error {
		var e error
		days, e = s.Usage.Daily(r.Context(), udb, tz)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, days)
}

// handleUsageLive reports how many sessions currently have connected clients:
// server-wide for owners/admins (scope "server"), else the caller's own.
func (s *Server) handleUsageLive(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	var st sessionhub.LiveStats
	scope := "user"
	if s.Hub != nil {
		if types.IsElevated(u.Role) {
			st, scope = s.Hub.Stats(0), "server"
		} else {
			st = s.Hub.Stats(u.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "live": st})
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	target := usageTarget(r)
	var from, to time.Time
	if v := r.URL.Query().Get("from"); v != "" {
		from, _ = time.Parse(time.RFC3339, v)
	}
	if v := r.URL.Query().Get("to"); v != "" {
		to, _ = time.Parse(time.RFC3339, v)
	}
	var rows []types.UsageRecord
	err := s.Storage.WithUser(r.Context(), target, func(udb *sql.DB) error {
		var e error
		rows, e = s.Usage.Query(r.Context(), udb, target, from, to)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireElevated(w, r); !ok {
		return
	}
	f := auditFilter(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	// Audit entries live in each user's database; aggregate across users.
	entries := []types.AuditEntry{}
	only := s.auditUserID(r)
	err := s.Storage.EachUser(r.Context(), func(userID int64, udb *sql.DB) error {
		if only != 0 && userID != only {
			return nil
		}
		es, e := s.Audit.Search(r.Context(), udb, userID, f, limit)
		entries = append(entries, es...)
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	writeJSON(w, http.StatusOK, entries)
}

// auditFilter reads q, action (repeatable), from/to (RFC3339) and tz (minutes).
func auditFilter(r *http.Request) audit.Filter {
	v := r.URL.Query()
	f := audit.Filter{Q: v.Get("q"), Actions: v["action"]}
	f.From, _ = time.Parse(time.RFC3339, v.Get("from"))
	f.To, _ = time.Parse(time.RFC3339, v.Get("to"))
	f.TZ, _ = strconv.Atoi(v.Get("tz"))
	return f
}

// auditUserID resolves the optional ?user=<email> filter (0 = everyone; an
// unknown email yields -1 so nothing matches).
func (s *Server) auditUserID(r *http.Request) int64 {
	email := strings.TrimSpace(r.URL.Query().Get("user"))
	if email == "" {
		return 0
	}
	us, _ := s.Auth.ListUsers(r.Context())
	for _, u := range us {
		if strings.EqualFold(u.Email, email) {
			return u.ID
		}
	}
	return -1
}

// handleAuditStats counts the entries matching the same filters as /audit, by
// action, user and day.
func (s *Server) handleAuditStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireElevated(w, r); !ok {
		return
	}
	f := auditFilter(r)
	emails := map[int64]string{}
	if us, err := s.Auth.ListUsers(r.Context()); err == nil {
		for _, u := range us {
			emails[u.ID] = u.Email
		}
	}
	actions, days, users := map[string]int{}, map[string]int{}, map[string]int{}
	total := 0
	only := s.auditUserID(r)
	err := s.Storage.EachUser(r.Context(), func(userID int64, udb *sql.DB) error {
		if only != 0 && userID != only {
			return nil
		}
		a, d, e := s.Audit.Stats(r.Context(), udb, f)
		for k, n := range a {
			actions[k] += n
			total += n
			users[emails[userID]] += n
		}
		for k, n := range d {
			days[k] += n
		}
		return e
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := types.AuditStats{Total: total, Actions: auditCounts(actions, true), Users: auditCounts(users, true), Days: auditCounts(days, false)}
	writeJSON(w, http.StatusOK, out)
}

func auditCounts(m map[string]int, byCount bool) []types.AuditCount {
	out := make([]types.AuditCount, 0, len(m))
	for k, n := range m {
		out = append(out, types.AuditCount{Key: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if byCount && out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// imageMime maps an image filename to its MIME type ("" if not a supported image).
// maxImageUploadBytes is the stored-size ceiling for uploaded images.
const maxImageUploadBytes = 4 << 20

func imageMime(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return ""
}
