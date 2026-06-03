package api

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/mcp"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// mcpScopeName reads the (scope, name) of a single server from the query string.
// Names are query-, not path-encoded so they may contain any characters.
func mcpScopeName(r *http.Request) (scope, name string) {
	q := r.URL.Query()
	return q.Get("scope"), q.Get("name")
}

// mcpNameRe restricts server names to a URL- and tool-namespace-safe charset.
var mcpNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validMCPURL reports whether s is an absolute http(s) URL.
func validMCPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// mcpScopeAllowed enforces who may mutate a registration at a given scope:
// shared requires owner/admin; user requires an authenticated user and that the
// server permits user-registered servers.
func (s *Server) mcpScopeAllowed(w http.ResponseWriter, r *http.Request, scope string) (*types.User, bool) {
	switch scope {
	case mcp.ScopeShared:
		return requireElevated(w, r)
	case mcp.ScopeUser:
		u, ok := auth.UserFromContext(r.Context())
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return nil, false
		}
		if !s.Cfg.MCP.AllowUserServersValue() {
			writeErr(w, http.StatusForbidden, "user-registered MCP servers are disabled")
			return nil, false
		}
		return u, true
	default:
		writeErr(w, http.StatusBadRequest, "invalid scope (use 'shared' or 'user')")
		return nil, false
	}
}

func toAPIMCPServer(srv mcp.Server, st mcp.Status) types.MCPServer {
	hasCred := srv.HeaderValue != "" || (srv.OAuth != nil && srv.OAuth.ClientID != "")
	return types.MCPServer{
		Scope:         srv.Scope,
		Name:          srv.Name,
		URL:           srv.URL,
		Transport:     srv.Transport,
		AuthType:      string(srv.AuthType),
		HeaderName:    srv.HeaderName,
		HasCredential: hasCred,
		Enabled:       srv.Enabled,
		AuthSatisfied: st.AuthSatisfied,
		NeedsAuth:     st.NeedsAuth,
		ToolCount:     st.ToolCount,
		Error:         st.Err,
	}
}

// handleListMCP lists shared + the caller's user MCP servers with status.
func (s *Server) handleListMCP(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	out := []types.MCPServer{}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		servers, err := s.MCP.Registry().ListVisible(r.Context(), udb)
		if err != nil {
			return err
		}
		// Probe statuses concurrently so several unreachable servers don't add up
		// to a request-killing delay (each probe is independently time-bounded).
		out = make([]types.MCPServer, len(servers))
		var wg sync.WaitGroup
		for i, srv := range servers {
			wg.Add(1)
			go func(i int, srv mcp.Server) {
				defer wg.Done()
				st := s.MCP.Status(r.Context(), u.ID, udb, srv)
				out[i] = toAPIMCPServer(srv, st)
			}(i, srv)
		}
		wg.Wait()
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetMCP returns one server with status.
func (s *Server) handleGetMCP(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	scope, name := mcpScopeName(r)
	var out types.MCPServer
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		srv, err := s.MCP.Registry().Get(r.Context(), scope, name, udb)
		if err != nil {
			return err
		}
		out = toAPIMCPServer(srv, s.MCP.Status(r.Context(), u.ID, udb, srv))
		// Include the tool list (cached by the status probe above) on the detail view.
		if out.AuthSatisfied && srv.Enabled {
			if tools, terr := s.MCP.Tools(r.Context(), u.ID, udb, srv); terr == nil {
				for _, t := range tools {
					out.Tools = append(out.Tools, types.MCPTool{Name: t.Name, Description: t.Description})
				}
			}
		}
		return nil
	})
	if errors.Is(err, mcp.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "mcp server not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRegisterMCP registers a new MCP server.
func (s *Server) handleRegisterMCP(w http.ResponseWriter, r *http.Request) {
	var req types.RegisterMCPRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Scope == "" {
		req.Scope = mcp.ScopeUser
	}
	u, ok := s.mcpScopeAllowed(w, r, req.Scope)
	if !ok {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	if req.Name == "" || req.URL == "" {
		writeErr(w, http.StatusBadRequest, "name and url are required")
		return
	}
	if !mcpNameRe.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, "invalid name: use letters, digits, '.', '_' or '-' (no spaces, slashes, or a URL)")
		return
	}
	if !validMCPURL(req.URL) {
		writeErr(w, http.StatusBadRequest, "invalid url: must be an absolute http(s) URL")
		return
	}
	if req.AuthType == "" {
		req.AuthType = string(mcp.AuthNone)
	}
	srv := mcp.Server{
		Scope:      req.Scope,
		Name:       req.Name,
		URL:        req.URL,
		Transport:  "http",
		AuthType:   mcp.AuthType(req.AuthType),
		HeaderName: req.HeaderName,
		CreatedBy:  u.ID,
		Enabled:    req.Enabled == nil || *req.Enabled,
	}
	switch srv.AuthType {
	case mcp.AuthNone:
	case mcp.AuthHeader:
		if req.HeaderValue == "" {
			writeErr(w, http.StatusBadRequest, "header_value is required for header auth")
			return
		}
		if !s.MCP.Registry().HasCipher() {
			writeErr(w, http.StatusBadRequest, "server has no encryption key configured (set HARLEQUIN_SECRET_KEY)")
			return
		}
		srv.HeaderValue = req.HeaderValue
	case mcp.AuthOAuth:
		if !s.MCP.Registry().HasCipher() {
			writeErr(w, http.StatusBadRequest, "server has no encryption key configured (set HARLEQUIN_SECRET_KEY)")
			return
		}
		srv.OAuth = &mcp.OAuthMeta{Scopes: req.OAuthScopes}
	default:
		writeErr(w, http.StatusBadRequest, "invalid auth_type")
		return
	}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		return s.MCP.Registry().Create(r.Context(), srv, udb)
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.MCP.Invalidate(srv.Scope, srv.Name)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}

// handleUpdateMCP updates url / enabled / header credential of a server.
func (s *Server) handleUpdateMCP(w http.ResponseWriter, r *http.Request) {
	scope, name := mcpScopeName(r)
	u, ok := s.mcpScopeAllowed(w, r, scope)
	if !ok {
		return
	}
	var req types.RegisterMCPRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		srv, err := s.MCP.Registry().Get(r.Context(), scope, name, udb)
		if err != nil {
			return err
		}
		if req.URL != "" {
			srv.URL = req.URL
		}
		if req.Enabled != nil {
			srv.Enabled = *req.Enabled
		}
		if req.HeaderValue != "" {
			srv.AuthType = mcp.AuthHeader
			if req.HeaderName != "" {
				srv.HeaderName = req.HeaderName
			}
			srv.HeaderValue = req.HeaderValue
		}
		return s.MCP.Registry().Update(r.Context(), srv, udb)
	})
	if errors.Is(err, mcp.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "mcp server not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.MCP.Invalidate(scope, name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDeleteMCP removes a server registration.
func (s *Server) handleDeleteMCP(w http.ResponseWriter, r *http.Request) {
	scope, name := mcpScopeName(r)
	u, ok := s.mcpScopeAllowed(w, r, scope)
	if !ok {
		return
	}
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		return s.MCP.Registry().Delete(r.Context(), scope, name, udb)
	})
	if errors.Is(err, mcp.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "mcp server not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.MCP.Invalidate(scope, name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleTestMCP connects and lists the server's tools.
func (s *Server) handleTestMCP(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	scope, name := mcpScopeName(r)
	var res types.MCPTestResult
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		srv, err := s.MCP.Registry().Get(r.Context(), scope, name, udb)
		if err != nil {
			return err
		}
		tools, err := s.MCP.Tools(r.Context(), u.ID, udb, srv)
		if err != nil {
			res.Error = err.Error()
			return nil
		}
		res.OK = true
		for _, t := range tools {
			res.Tools = append(res.Tools, t.Name)
		}
		return nil
	})
	if errors.Is(err, mcp.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "mcp server not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleMCPOAuthStart begins the OAuth flow and returns an authorize URL.
func (s *Server) handleMCPOAuthStart(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	scope, name := mcpScopeName(r)
	var authURL string
	err := s.Storage.WithUser(r.Context(), u.ID, func(udb *sql.DB) error {
		var e error
		authURL, e = s.MCP.StartAuth(r.Context(), u.ID, udb, scope, name)
		return e
	})
	if errors.Is(err, mcp.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "mcp server not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, types.MCPAuthStartResult{AuthorizeURL: authURL})
}

// handleMCPOAuthCallback completes the OAuth flow. It is browser-facing: the
// `state` parameter (bound to a pending authorization) authenticates the request.
func (s *Server) handleMCPOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errMsg := q.Get("error"); errMsg != "" {
		writeMCPCallbackPage(w, false, "Authorization failed: "+errMsg)
		return
	}
	state, code := q.Get("state"), q.Get("code")
	if state == "" || code == "" {
		writeMCPCallbackPage(w, false, "Missing code or state.")
		return
	}
	userID, ok := s.MCP.PendingUser(state)
	if !ok {
		writeMCPCallbackPage(w, false, "Unknown or expired authorization request.")
		return
	}
	err := s.Storage.WithUser(r.Context(), userID, func(udb *sql.DB) error {
		return s.MCP.CompleteAuth(r.Context(), udb, state, code)
	})
	if err != nil {
		writeMCPCallbackPage(w, false, "Could not complete authorization: "+err.Error())
		return
	}
	writeMCPCallbackPage(w, true, "Authorization complete. You can close this window and return to Harlequin.")
}

func writeMCPCallbackPage(w http.ResponseWriter, ok bool, msg string) {
	status := http.StatusOK
	if !ok {
		status = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	title := "Success"
	if !ok {
		title = "Error"
	}
	_, _ = w.Write([]byte("<!doctype html><html><head><meta charset=\"utf-8\"><title>MCP " + title +
		"</title></head><body style=\"font-family:sans-serif;max-width:40rem;margin:4rem auto;\"><h1>MCP " +
		title + "</h1><p>" + htmlEscape(msg) + "</p></body></html>"))
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return r.Replace(s)
}
