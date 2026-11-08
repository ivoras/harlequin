package mcp

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

// ErrNeedAuth is returned when a server requires OAuth authorization that the
// user has not yet completed.
var ErrNeedAuth = errors.New("mcp: authorization required")

const oauthCallbackPath = "/api/v1/mcp/oauth/callback"

// ManagerConfig tunes the Manager.
type ManagerConfig struct {
	SessionIdle     time.Duration
	ToolsCacheTTL   time.Duration
	CallbackBaseURL string // e.g. https://harlequin.example.com
	ClientName      string // advertised to MCP servers (e.g. "harlequin")
	ClientVersion   string
	// DialTimeout bounds the TCP connect (and response-header wait) to an MCP
	// server. The MCP SDK does not honour the per-call context for the initial
	// connect, so this is what actually prevents an unreachable server from
	// hanging a request. Default 5s.
	DialTimeout time.Duration
	// WithUser opens a user's database for the duration of fn. Token refreshes
	// happen inside pooled sessions long after the request that dialed them
	// closed its handle, so rotated tokens are persisted through this.
	WithUser func(ctx context.Context, userID int64, fn func(userDB *sql.DB) error) error
	// OnAuthLost is called (once per loss; the dead token is deleted first) when
	// a user's OAuth sign-in can no longer be refreshed. May be nil.
	OnAuthLost func(ctx context.Context, userID int64, userDB *sql.DB, srv Server)
}

// Manager dials and pools MCP sessions, caches tool lists, and drives the OAuth
// authorization flow. It is safe for concurrent use.
type Manager struct {
	reg  *Registry
	cfg  ManagerConfig
	base http.RoundTripper

	mu       sync.Mutex
	sessions map[string]*pooledSession
	tools    map[string]*toolsCacheEntry
	pending  map[string]*pendingAuth
	// One refreshing token source per (user, server): separate sources would
	// each refresh the same stored token, and a rotating provider rejects the
	// second use of a spent refresh token (invalid_grant).
	tokenSrcs map[string]oauth2.TokenSource
}

type pooledSession struct {
	session  *mcpsdk.ClientSession
	lastUsed time.Time
}

type toolsCacheEntry struct {
	tools   []Tool
	fetched time.Time
}

type pendingAuth struct {
	userID      int64
	scope, name string
	verifier    string
	redirectURL string // must match between authorize and token exchange
	returnTo    string
	expires     time.Time
}

// PendingAuth identifies an in-flight authorization for the callback handler.
type PendingAuth struct {
	UserID   int64
	Scope    string
	Name     string
	ReturnTo string // relative web-UI path to send the browser back to ("" = none)
}

// NewManager constructs a Manager. base may be nil, in which case a transport
// with a bounded dial timeout (cfg.DialTimeout) is used so unreachable servers
// fail fast — the MCP SDK ignores the per-call context for the initial connect.
func NewManager(reg *Registry, cfg ManagerConfig, base http.RoundTripper) *Manager {
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if base == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.DialContext = (&net.Dialer{Timeout: cfg.DialTimeout, KeepAlive: 30 * time.Second}).DialContext
		t.ResponseHeaderTimeout = cfg.DialTimeout
		base = t
	}
	if cfg.SessionIdle <= 0 {
		cfg.SessionIdle = 5 * time.Minute
	}
	if cfg.ToolsCacheTTL <= 0 {
		cfg.ToolsCacheTTL = 5 * time.Minute
	}
	if cfg.ClientName == "" {
		cfg.ClientName = "harlequin"
	}
	return &Manager{
		reg:       reg,
		cfg:       cfg,
		base:      base,
		sessions:  map[string]*pooledSession{},
		tools:     map[string]*toolsCacheEntry{},
		pending:   map[string]*pendingAuth{},
		tokenSrcs: map[string]oauth2.TokenSource{},
	}
}

// Registry exposes the underlying registry for CRUD from the API layer.
func (m *Manager) Registry() *Registry { return m.reg }

func key(userID int64, scope, name string) string {
	return fmt.Sprintf("%d|%s|%s", userID, scope, name)
}

// authSatisfied reports whether we hold the credentials to connect to srv.
func (m *Manager) authSatisfied(ctx context.Context, userDB *sql.DB, srv Server) bool {
	switch srv.AuthType {
	case AuthNone:
		return true
	case AuthHeader:
		return len(srv.Headers) > 0
	case AuthOAuth:
		tok, err := m.reg.LoadToken(ctx, userDB, srv.Scope, srv.Name)
		// Some providers issue long-lived access tokens with no refresh token.
		return err == nil && tok != nil && (tok.RefreshToken != "" ||
			(tok.AccessToken != "" && (tok.Expiry.IsZero() || time.Now().Before(tok.Expiry))))
	}
	return false
}

// httpClientFor returns an auth-injecting HTTP client for srv.
func (m *Manager) httpClientFor(ctx context.Context, userID int64, userDB *sql.DB, srv Server) (*http.Client, error) {
	switch srv.AuthType {
	case AuthNone:
		return &http.Client{Transport: m.base, Timeout: 60 * time.Second}, nil
	case AuthHeader:
		if len(srv.Headers) == 0 {
			return nil, ErrNeedAuth
		}
		return &http.Client{Transport: &headerRT{headers: srv.Headers, base: m.base}, Timeout: 60 * time.Second}, nil
	case AuthOAuth:
		tok, err := m.reg.LoadToken(ctx, userDB, srv.Scope, srv.Name)
		if err != nil {
			return nil, err
		}
		if tok == nil {
			return nil, ErrNeedAuth
		}
		if tok.RefreshToken == "" && !tok.Expiry.IsZero() && time.Now().After(tok.Expiry) {
			m.authLost(userID, userDB, srv, "access token expired and no refresh token was issued")
			return nil, ErrNeedAuth
		}
		hc := &http.Client{Transport: m.base}
		ts := m.sharedTokenSource(key(userID, srv.Scope, srv.Name), func() oauth2.TokenSource {
			return tokenSource(ctx, hc, m, userID, srv, tok)
		})
		return &http.Client{Transport: &oauth2.Transport{Source: ts, Base: m.base}, Timeout: 60 * time.Second}, nil
	}
	return nil, fmt.Errorf("mcp: unknown auth type %q", srv.AuthType)
}

// connect dials a fresh session (no pooling).
func (m *Manager) connect(ctx context.Context, userID int64, userDB *sql.DB, srv Server) (*mcpsdk.ClientSession, error) {
	hc, err := m.httpClientFor(ctx, userID, userDB, srv)
	if err != nil {
		return nil, err
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: m.cfg.ClientName, Version: m.cfg.ClientVersion}, nil)
	// We only issue request/response tool calls, so we don't need the standalone
	// server->client SSE stream; disabling it removes an extra dial (faster
	// failure detection for unreachable servers) and per-connection overhead.
	transport := &mcpsdk.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: hc, DisableStandaloneSSE: true}
	return client.Connect(ctx, transport, nil)
}

// session returns a pooled session for (userID, srv), dialing if needed.
func (m *Manager) session(ctx context.Context, userID int64, userDB *sql.DB, srv Server) (*mcpsdk.ClientSession, error) {
	k := key(userID, srv.Scope, srv.Name)
	m.mu.Lock()
	if ps, ok := m.sessions[k]; ok {
		if time.Since(ps.lastUsed) < m.cfg.SessionIdle {
			ps.lastUsed = time.Now()
			m.mu.Unlock()
			return ps.session, nil
		}
		delete(m.sessions, k)
		go ps.session.Close()
	}
	m.mu.Unlock()

	sess, err := m.connect(ctx, userID, userDB, srv)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.sessions[k] = &pooledSession{session: sess, lastUsed: time.Now()}
	m.mu.Unlock()
	return sess, nil
}

// Tools returns srv's advertised tools, cached for ToolsCacheTTL.
func (m *Manager) Tools(ctx context.Context, userID int64, userDB *sql.DB, srv Server) ([]Tool, error) {
	k := key(userID, srv.Scope, srv.Name)
	m.mu.Lock()
	if e, ok := m.tools[k]; ok && time.Since(e.fetched) < m.cfg.ToolsCacheTTL {
		t := e.tools
		m.mu.Unlock()
		return t, nil
	}
	m.mu.Unlock()

	sess, err := m.session(ctx, userID, userDB, srv)
	if err != nil {
		return nil, err
	}
	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		m.drop(k)
		return nil, err
	}
	out := make([]Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		schema, _ := t.InputSchema.(map[string]any)
		out = append(out, Tool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	m.mu.Lock()
	m.tools[k] = &toolsCacheEntry{tools: out, fetched: time.Now()}
	m.mu.Unlock()
	return out, nil
}

// Call invokes a tool and returns its flattened text content.
func (m *Manager) Call(ctx context.Context, userID int64, userDB *sql.DB, srv Server, tool string, args map[string]any) (string, error) {
	sess, err := m.session(ctx, userID, userDB, srv)
	if err != nil {
		return "", err
	}
	res, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		m.drop(key(userID, srv.Scope, srv.Name))
		return "", err
	}
	text := flattenContent(res.Content)
	if res.IsError {
		if text == "" {
			text = "(tool reported an error)"
		}
		return "error: " + text, nil
	}
	return text, nil
}

// statusProbeTimeout bounds the live connection a status probe makes, so a single
// unreachable server can't stall a /mcp listing.
const statusProbeTimeout = 6 * time.Second

// Status probes whether a server is usable for the given user (without forcing a
// network call when auth is plainly missing). The live probe is time-bounded.
func (m *Manager) Status(ctx context.Context, userID int64, userDB *sql.DB, srv Server) Status {
	st := Status{Enabled: srv.Enabled}
	st.AuthSatisfied = m.authSatisfied(ctx, userDB, srv)
	if srv.AuthType == AuthOAuth && !st.AuthSatisfied {
		st.NeedsAuth = true
	}
	if !srv.Enabled || !st.AuthSatisfied {
		return st
	}
	probeCtx, cancel := context.WithTimeout(ctx, statusProbeTimeout)
	defer cancel()
	tools, err := m.Tools(probeCtx, userID, userDB, srv)
	if err != nil {
		st.Err = err.Error()
		return st
	}
	st.ToolCount = len(tools)
	return st
}

// sharedTokenSource returns the cached source for k, building it on first use.
func (m *Manager) sharedTokenSource(k string, build func() oauth2.TokenSource) oauth2.TokenSource {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ts, ok := m.tokenSrcs[k]; ok {
		return ts
	}
	ts := build()
	m.tokenSrcs[k] = ts
	return ts
}

func (m *Manager) drop(k string) {
	m.mu.Lock()
	delete(m.tokenSrcs, k)
	if ps, ok := m.sessions[k]; ok {
		delete(m.sessions, k)
		go ps.session.Close()
	}
	delete(m.tools, k)
	m.mu.Unlock()
}

// invalidate drops cached sessions/tools for a server across all users (e.g.
// after the registration changes).
func (m *Manager) invalidate(scope, name string) {
	suffix := "|" + scope + "|" + name
	m.mu.Lock()
	for k, ps := range m.sessions {
		if hasSuffix(k, suffix) {
			delete(m.sessions, k)
			go ps.session.Close()
		}
	}
	for k := range m.tools {
		if hasSuffix(k, suffix) {
			delete(m.tools, k)
		}
	}
	for k := range m.tokenSrcs { // re-seeded from the stored token on next use
		if hasSuffix(k, suffix) {
			delete(m.tokenSrcs, k)
		}
	}
	m.mu.Unlock()
}

// authLost deletes a user's dead OAuth token, drops their pooled session and
// reports it. userDB may be nil (refresh inside a pooled session): it is then
// opened via cfg.WithUser.
func (m *Manager) authLost(userID int64, userDB *sql.DB, srv Server, reason string) {
	log.Printf("mcp: user %d sign-in to %s/%s lost (%s); token removed", userID, srv.Scope, srv.Name, reason)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.drop(key(userID, srv.Scope, srv.Name))
	fn := func(udb *sql.DB) error {
		if err := m.reg.DeleteToken(ctx, udb, srv.Scope, srv.Name); err != nil {
			return err
		}
		if m.cfg.OnAuthLost != nil {
			m.cfg.OnAuthLost(ctx, userID, udb, srv)
		}
		return nil
	}
	var err error
	if userDB != nil {
		err = fn(userDB)
	} else {
		err = m.withUser(ctx, userID, fn)
	}
	if err != nil {
		log.Printf("mcp: clearing lost token for user %d %s/%s: %v", userID, srv.Scope, srv.Name, err)
	}
}

func (m *Manager) withUser(ctx context.Context, userID int64, fn func(*sql.DB) error) error {
	if m.cfg.WithUser == nil {
		return errors.New("mcp: no user database opener configured")
	}
	return m.cfg.WithUser(ctx, userID, fn)
}

// Invalidate is the exported entry point used by the API layer after mutations.
func (m *Manager) Invalidate(scope, name string) { m.invalidate(scope, name) }

// Close shuts down all pooled sessions.
func (m *Manager) Close() {
	m.mu.Lock()
	for k, ps := range m.sessions {
		delete(m.sessions, k)
		ps.session.Close()
	}
	m.mu.Unlock()
}

// --- OAuth orchestration ---

// redirectURL builds the OAuth redirect URI. The configured base URL wins;
// otherwise the caller-derived one (the URL the user reached us through) is used.
func (m *Manager) redirectURL(fallbackBase string) string {
	base := m.cfg.CallbackBaseURL
	if base == "" {
		base = fallbackBase
	}
	return strings.TrimRight(base, "/") + oauthCallbackPath
}

// StartAuth begins the OAuth flow for an oauth server: it discovers endpoints
// (and registers a client) if needed, persists any newly-learned metadata, and
// returns an authorization URL for the user to open. The user's database is
// needed because shared-server tokens are also stored per-user. fallbackBase is
// used when no callback base URL is configured; returnTo (a relative path, may
// be empty) is where the callback sends the browser afterwards.
func (m *Manager) StartAuth(ctx context.Context, userID int64, userDB *sql.DB, scope, name, fallbackBase, returnTo string) (string, error) {
	redirect := m.redirectURL(fallbackBase)
	if !strings.HasPrefix(redirect, "http://") && !strings.HasPrefix(redirect, "https://") {
		return "", errors.New("mcp: cannot determine the OAuth callback URL; set mcp.oauth_callback_base_url")
	}
	srv, err := m.reg.Get(ctx, scope, name, userDB)
	if err != nil {
		return "", err
	}
	if srv.AuthType != AuthOAuth {
		return "", fmt.Errorf("mcp: server %q does not use OAuth", name)
	}
	hc := &http.Client{Transport: m.base, Timeout: 30 * time.Second}

	meta, err := discoverOAuth(ctx, hc, srv.URL, srv.OAuth)
	if err != nil {
		return "", err
	}
	// A dynamically registered client is bound to its redirect URI; if we're now
	// reached through a different URL, register a fresh client.
	if meta.RegistrationEndpoint != "" && meta.RedirectURI != "" && meta.RedirectURI != redirect {
		meta.ClientID, meta.ClientSecret = "", ""
	}
	cid, csecret, err := registerClient(ctx, hc, meta, m.cfg.ClientName, redirect)
	if err != nil {
		return "", err
	}
	meta.ClientID, meta.ClientSecret, meta.RedirectURI = cid, csecret, redirect
	if meta.ClientID == "" {
		return "", errors.New("mcp: no OAuth client_id (provider requires manual client registration)")
	}
	// Persist discovered metadata + client credentials for future connects.
	srv.OAuth = meta
	if err := m.reg.Update(ctx, srv, userDB); err != nil {
		return "", err
	}

	verifier := oauth2.GenerateVerifier()
	state := randToken()
	now := time.Now()
	m.mu.Lock()
	for k, p := range m.pending { // abandoned flows would otherwise accumulate
		if now.After(p.expires) {
			delete(m.pending, k)
		}
	}
	m.pending[state] = &pendingAuth{userID: userID, scope: scope, name: name, verifier: verifier,
		redirectURL: redirect, returnTo: returnTo, expires: now.Add(10 * time.Minute)}
	m.mu.Unlock()

	return authCodeURL(meta, redirect, state, verifier), nil
}

// Pending returns the pending authorization for state, so the callback handler
// can open the right user database and send the browser back.
func (m *Manager) Pending(state string) (PendingAuth, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[state]
	if !ok || time.Now().After(p.expires) {
		return PendingAuth{}, false
	}
	return PendingAuth{UserID: p.userID, Scope: p.scope, Name: p.name, ReturnTo: p.returnTo}, true
}

// CompleteAuth exchanges the authorization code and stores the resulting tokens
// in the user's database.
func (m *Manager) CompleteAuth(ctx context.Context, userDB *sql.DB, state, code string) error {
	m.mu.Lock()
	p, ok := m.pending[state]
	if ok {
		delete(m.pending, state)
	}
	m.mu.Unlock()
	if !ok || time.Now().After(p.expires) {
		return errors.New("mcp: unknown or expired authorization state")
	}
	srv, err := m.reg.Get(ctx, p.scope, p.name, userDB)
	if err != nil {
		return err
	}
	hc := &http.Client{Transport: m.base, Timeout: 30 * time.Second}
	tok, err := exchangeCode(ctx, hc, srv.OAuth, p.redirectURL, code, p.verifier)
	if err != nil {
		return err
	}
	if err := m.reg.SaveToken(ctx, userDB, p.scope, p.name, tok); err != nil {
		return err
	}
	m.invalidate(p.scope, p.name)
	return nil
}

// --- helpers ---

type headerRT struct {
	headers []Header
	base    http.RoundTripper
}

func (h *headerRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	for _, hd := range h.headers {
		name := hd.Name
		if name == "" {
			name = "Authorization"
		}
		r.Header.Set(name, hd.Value)
	}
	return h.base.RoundTrip(r)
}

func flattenContent(content []mcpsdk.Content) string {
	var b []byte
	for _, c := range content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			if len(b) > 0 {
				b = append(b, '\n')
			}
			b = append(b, tc.Text...)
		}
	}
	return string(b)
}

func randToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
