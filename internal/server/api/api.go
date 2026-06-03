// Package api wires the HTTP routes (chi) and the SSE writer for streamed
// agent responses.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/ivoras/harlequin/internal/server/agent"
	"github.com/ivoras/harlequin/internal/server/audit"
	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/conversation"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/mcp"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/server/usage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Server holds the dependencies for the HTTP handlers.
type Server struct {
	Cfg           *config.Config
	Storage       *storage.Manager
	Auth          *auth.Store
	Conversations *conversation.Store
	Memory        *memory.Store
	Docs          *documents.Store
	Skills        *skills.Manager
	Usage         *usage.Store
	Audit         *audit.Store
	Session       *sessionlog.Logger
	Agent         *agent.Agent
	MCP           *mcp.Manager
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
		// OAuth redirect target for MCP authorization (browser-facing; the state
		// parameter authenticates the request, so it is outside the bearer group).
		if s.MCP != nil {
			r.Get("/mcp/oauth/callback", s.handleMCPOAuthCallback)
		}

		// Authenticated routes.
		r.Group(func(r chi.Router) {
			r.Use(s.Auth.Middleware)

			r.Post("/auth/logout", s.handleLogout)
			r.Get("/me", s.handleMe)
			r.Post("/users", s.handleCreateUser)

			r.Get("/conversations", s.handleListConversations)
			r.Post("/conversations", s.handleCreateConversation)
			r.Get("/conversations/{id}/messages", s.handleListMessages)
			r.Post("/conversations/{id}/messages", s.handleSendMessage)
			r.Delete("/conversations/{id}", s.handleDeleteConversation)
			r.Get("/conversations/{id}/log", s.handleConversationLog)
			r.Post("/conversations/{id}/hat", s.handleSetConversationHat)

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

			r.Get("/usage", s.handleUsage)
			r.Get("/audit", s.handleAudit)
			r.Post("/reload", s.handleReload)
		})
	})

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
