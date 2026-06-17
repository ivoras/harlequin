// Package api wires the HTTP routes (chi) and the SSE writer for streamed
// agent responses.
package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/ivoras/harlequin/internal/server/agent"
	"github.com/ivoras/harlequin/internal/server/audit"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/cron"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/email"
	"github.com/ivoras/harlequin/internal/server/mcp"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/notify"
	"github.com/ivoras/harlequin/internal/server/pdfextract"
	"github.com/ivoras/harlequin/internal/server/presence"
	"github.com/ivoras/harlequin/internal/server/project"
	"github.com/ivoras/harlequin/internal/server/session"
	"github.com/ivoras/harlequin/internal/server/sessionhub"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/server/usage"
	"github.com/ivoras/harlequin/internal/server/userconfig"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Server holds the dependencies for the HTTP handlers.
type Server struct {
	Cfg        *config.Config
	Storage    *storage.Manager
	Auth       *auth.Store
	Sessions   *session.Store
	Memory     *memory.Store
	Docs       *documents.Store
	Skills     *skills.Manager
	Usage      *usage.Store
	Audit      *audit.Store
	Session    *sessionlog.Logger
	Agent      *agent.Agent
	MCP        *mcp.Manager
	Notify     *notify.Store
	Cron       *cron.Store
	CronSched  *cron.Scheduler
	UserConfig *userconfig.Store
	Presence   *presence.Tracker
	Email      *email.Sender
	PDFExtract *pdfextract.Extractor
	Hub        *sessionhub.Hub
	Projects   *project.Store
}

// Router builds the chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/auth/login", s.handleLogin)
		// Public self-registration (gated by config; the handlers themselves
		// enforce auth.allow_registration).
		r.Get("/auth/registration", s.handleRegistrationStatus)
		r.Post("/auth/register", s.handleRegister)
		r.Post("/auth/verify", s.handleVerify)
		// OAuth redirect target for MCP authorization (browser-facing; the state
		// parameter authenticates the request, so it is outside the bearer group).
		if s.MCP != nil {
			r.Get("/mcp/oauth/callback", s.handleMCPOAuthCallback)
		}

		// Live session WebSocket: streaming + prompt submission. Outside the bearer
		// middleware group because browsers cannot set the Authorization header on a
		// WebSocket; handleSessionWS authenticates from the header or the
		// `bearer.<token>` subprotocol itself.
		if s.Hub != nil {
			r.Get("/sessions/{id}/ws", s.handleSessionWS)
			if s.Projects != nil {
				r.Get("/projects/{id}/sessions/{sid}/ws", s.handleProjectSessionWS)
			}
		}

		// Authenticated routes.
		r.Group(func(r chi.Router) {
			r.Use(s.Auth.Middleware)
			r.Use(s.touchPresence)

			r.Post("/auth/logout", s.handleLogout)
			r.Get("/me", s.handleMe)
			r.Post("/users", s.handleCreateUser)

			r.Get("/sessions", s.handleListSessions)
			r.Post("/sessions", s.handleCreateSession)
			r.Get("/sessions/{id}/messages", s.handleListMessages)
			r.Delete("/sessions/{id}", s.handleDeleteSession)
			r.Get("/sessions/{id}/log", s.handleSessionLog)
			r.Post("/sessions/{id}/hat", s.handleSetSessionHat)

			r.Get("/hats", s.handleListHats)
			r.Get("/hats/{name}", s.handleGetHat)

			r.Get("/skills", s.handleListSkills)
			r.Get("/skills/{name}", s.handleGetSkill)
			r.Put("/skills/{name}", s.handlePutSkill)
			r.Delete("/skills/{name}", s.handleDeleteSkill)
			r.Post("/skills/{name}/publish", s.handlePublishSkill)

			r.Get("/memory", s.handleListMemory)
			r.Get("/memory/conflicts", s.handleListMemoryConflicts)
			r.Get("/memory/search", s.handleSearchMemory)
			r.Get("/memory/find", s.handleFindMemory)
			r.Get("/memory/{id}", s.handleGetMemory)
			r.Post("/memory", s.handleCreateMemory)
			r.Patch("/memory/{id}", s.handlePatchMemory)
			r.Delete("/memory/{id}", s.handleDeleteMemory)
			r.Post("/memory/conflicts/{id}/resolve", s.handleResolveMemoryConflict)

			r.Get("/documents", s.handleListDocuments)
			r.Post("/documents", s.handleCreateDocument)
			r.Delete("/documents/{id}", s.handleDeleteDocument)
			r.Get("/documents/search", s.handleSearchDocuments)

			if s.MCP != nil {
				// A server is addressed by ?scope=&name= (not a path segment) so
				// arbitrary names encode safely.
				r.Get("/mcp", s.handleListMCP)
				r.Post("/mcp", s.handleRegisterMCP)
				r.Get("/mcp/server", s.handleGetMCP)
				r.Patch("/mcp/server", s.handleUpdateMCP)
				r.Delete("/mcp/server", s.handleDeleteMCP)
				r.Post("/mcp/server/test", s.handleTestMCP)
				r.Post("/mcp/server/oauth/start", s.handleMCPOAuthStart)
			}

			r.Get("/notifications", s.handleListNotifications)
			r.Post("/notifications/{id}/ack", s.handleAckNotification)
			r.Post("/notifications/{id}/dismiss", s.handleDismissNotification)
			// Owner/admin broadcast alert to all users (handler enforces the role).
			r.Post("/alerts", s.handleBroadcastAlert)

			if s.Projects != nil {
				// Static "/projects/invites" is registered before "/projects/{id}"
				// so it isn't captured by the id param.
				r.Get("/projects", s.handleListProjects)
				r.Post("/projects", s.handleCreateProject)
				r.Get("/projects/invites", s.handleListProjectInvites)
				r.Post("/projects/invites/{inviteID}/accept", s.handleAcceptInvite)
				r.Post("/projects/invites/{inviteID}/decline", s.handleDeclineInvite)
				r.Get("/projects/{id}", s.handleGetProject)
				r.Post("/projects/{id}/invite", s.handleInviteProject)
				r.Get("/projects/{id}/sessions", s.handleListProjectSessions)
				r.Post("/projects/{id}/sessions/{sid}", s.handleAssignSession)
				r.Get("/projects/{id}/messages", s.handleProjectMessages)
			}

			if s.UserConfig != nil {
				r.Get("/config", s.handleGetConfig)
				r.Put("/config/{key}", s.handleSetConfig)
				r.Delete("/config/{key}", s.handleDeleteConfig)
			}

			if s.Cron != nil {
				r.Get("/cron", s.handleListCron)
				r.Post("/cron", s.handleCreateCron)
				r.Get("/cron/{id}", s.handleGetCron)
				r.Patch("/cron/{id}", s.handleUpdateCron)
				r.Delete("/cron/{id}", s.handleDeleteCron)
				r.Post("/cron/{id}/run", s.handleRunCron)
			}

			r.Get("/usage", s.handleUsage)
			r.Get("/audit", s.handleAudit)
			r.Post("/reload", s.handleReload)
		})
	})

	// Optionally serve the static web UI at / (same origin as the API). The
	// /api/v1 routes above are matched first; everything else falls through to the
	// SPA handler.
	if s.Cfg != nil && s.Cfg.Server.Web.Dir != "" {
		r.Handle("/*", spaHandler(s.Cfg.Server.Web.Dir))
	}

	return r
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, types.ErrorResponse{Error: msg})
}

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// reqInterface returns the interface a REST client announced via the
// X-Harlequin-Interface header, defaulting to the TUI (the built-in client).
func reqInterface(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get(types.HeaderInterface)); v != "" {
		return v
	}
	return types.InterfaceTUI
}

// touchPresence records each authenticated request as a heartbeat for the
// caller's (user, interface), so background tasks can tell which interfaces are
// live. Runs after the auth middleware (user is in context).
func (s *Server) touchPresence(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Presence != nil {
			if u, ok := auth.UserFromContext(r.Context()); ok {
				s.Presence.Touch(u.ID, reqInterface(r))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireElevated allows owners and admins (org-wide administrative actions).
func requireElevated(w http.ResponseWriter, r *http.Request) (*types.User, bool) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	if !types.IsElevated(u.Role) {
		writeErr(w, http.StatusForbidden, "owner or admin required")
		return nil, false
	}
	return u, true
}

// requireOwner allows only the owner (user management).
func requireOwner(w http.ResponseWriter, r *http.Request) (*types.User, bool) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	if !types.IsOwner(u.Role) {
		writeErr(w, http.StatusForbidden, "owner required")
		return nil, false
	}
	return u, true
}
