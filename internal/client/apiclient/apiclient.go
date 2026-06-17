// Package apiclient is the client-side REST + WebSocket client for the Harlequin
// server. Chat streaming runs over a per-session WebSocket; everything else is REST.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Client talks to the Harlequin server.
type Client struct {
	baseURL string
	token   string
	iface   string // the interface this client announces (e.g. "TUI")
	http    *http.Client
}

// New constructs a Client that announces itself as the given interface (e.g.
// types.InterfaceTUI); the server records it per session.
func New(baseURL, token, iface string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		iface:   iface,
		http:    &http.Client{Timeout: 0},
	}
}

// SetToken updates the auth token.
func (c *Client) SetToken(token string) { c.token = token }

// Token returns the current auth token.
func (c *Client) Token() string { return c.token }

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/api/v1"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.iface != "" {
		req.Header.Set(types.HeaderInterface, c.iface)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var er types.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&er)
		if er.Error == "" {
			er.Error = resp.Status
		}
		return fmt.Errorf("%s", er.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Login authenticates (by email) and returns the issued token.
func (c *Client) Login(ctx context.Context, email, password string) (*types.LoginResponse, error) {
	var resp types.LoginResponse
	if err := c.do(ctx, http.MethodPost, "/auth/login", types.LoginRequest{Email: email, Password: password}, &resp); err != nil {
		return nil, err
	}
	c.token = resp.Token
	return &resp, nil
}

// Register starts self-registration: the server emails a magic code to verify.
func (c *Client) Register(ctx context.Context, email, password string) (*types.RegisterResponse, error) {
	var resp types.RegisterResponse
	if err := c.do(ctx, http.MethodPost, "/auth/register", types.RegisterRequest{Email: email, Password: password}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Verify completes registration with the emailed code, returning a login token.
func (c *Client) Verify(ctx context.Context, email, code string) (*types.LoginResponse, error) {
	var resp types.LoginResponse
	if err := c.do(ctx, http.MethodPost, "/auth/verify", types.VerifyRequest{Email: email, Code: code}, &resp); err != nil {
		return nil, err
	}
	c.token = resp.Token
	return &resp, nil
}

// RegistrationEnabled reports whether self-registration is available.
func (c *Client) RegistrationEnabled(ctx context.Context) (bool, error) {
	var resp types.RegistrationStatus
	if err := c.do(ctx, http.MethodGet, "/auth/registration", nil, &resp); err != nil {
		return false, err
	}
	return resp.Enabled, nil
}

// Me returns the current user.
func (c *Client) Me(ctx context.Context) (*types.User, error) {
	var u types.User
	return &u, c.do(ctx, http.MethodGet, "/me", nil, &u)
}

// ListSessions returns sessions (optionally filtered).
func (c *Client) ListSessions(ctx context.Context, q string) ([]types.Session, error) {
	var out []types.Session
	path := "/sessions"
	if q != "" {
		path += "?q=" + q
	}
	return out, c.do(ctx, http.MethodGet, path, nil, &out)
}

// CreateSession starts a session.
func (c *Client) CreateSession(ctx context.Context, title, hat string) (*types.Session, error) {
	var sess types.Session
	return &sess, c.do(ctx, http.MethodPost, "/sessions", types.CreateSessionRequest{Title: title, Hat: hat}, &sess)
}

// Reload expires the server's .md source-file cache (skills, system prompts,
// hat data). Owner/admin only.
func (c *Client) Reload(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/reload", nil, nil)
}

// ListHats returns the deployed hats.
func (c *Client) ListHats(ctx context.Context) ([]types.Hat, error) {
	var out []types.Hat
	return out, c.do(ctx, http.MethodGet, "/hats", nil, &out)
}

// GetHat returns one hat by name.
func (c *Client) GetHat(ctx context.Context, name string) (*types.Hat, error) {
	var out types.Hat
	return &out, c.do(ctx, http.MethodGet, "/hats/"+name, nil, &out)
}

// SetSessionHat sets (or clears, when hat is empty) the session's hat.
func (c *Client) SetSessionHat(ctx context.Context, sessionID int64, hat string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/sessions/%d/hat", sessionID),
		types.SetSessionHatRequest{Hat: hat}, nil)
}

// ListMCP returns the visible MCP servers (shared + the user's own) with status.
func (c *Client) ListMCP(ctx context.Context) ([]types.MCPServer, error) {
	var out []types.MCPServer
	return out, c.do(ctx, http.MethodGet, "/mcp", nil, &out)
}

// mcpItemQuery builds the ?scope=&name= query addressing a single server, so
// names with any characters encode safely.
func mcpItemQuery(scope, name string) string {
	v := url.Values{}
	v.Set("scope", scope)
	v.Set("name", name)
	return "?" + v.Encode()
}

// GetMCP returns one MCP server.
func (c *Client) GetMCP(ctx context.Context, scope, name string) (*types.MCPServer, error) {
	var out types.MCPServer
	return &out, c.do(ctx, http.MethodGet, "/mcp/server"+mcpItemQuery(scope, name), nil, &out)
}

// RegisterMCP registers a new MCP server.
func (c *Client) RegisterMCP(ctx context.Context, req types.RegisterMCPRequest) error {
	return c.do(ctx, http.MethodPost, "/mcp", req, nil)
}

// UpdateMCP updates an MCP server (url / enabled / header credential).
func (c *Client) UpdateMCP(ctx context.Context, scope, name string, req types.RegisterMCPRequest) error {
	return c.do(ctx, http.MethodPatch, "/mcp/server"+mcpItemQuery(scope, name), req, nil)
}

// DeleteMCP removes an MCP server.
func (c *Client) DeleteMCP(ctx context.Context, scope, name string) error {
	return c.do(ctx, http.MethodDelete, "/mcp/server"+mcpItemQuery(scope, name), nil, nil)
}

// TestMCP connects to a server and returns its tool names.
func (c *Client) TestMCP(ctx context.Context, scope, name string) (*types.MCPTestResult, error) {
	var out types.MCPTestResult
	return &out, c.do(ctx, http.MethodPost, "/mcp/server/test"+mcpItemQuery(scope, name), nil, &out)
}

// StartMCPOAuth begins the OAuth flow and returns the authorize URL.
func (c *Client) StartMCPOAuth(ctx context.Context, scope, name string) (*types.MCPAuthStartResult, error) {
	var out types.MCPAuthStartResult
	return &out, c.do(ctx, http.MethodPost, "/mcp/server/oauth/start"+mcpItemQuery(scope, name), nil, &out)
}

// ListNotifications returns the caller's pending notifications.
func (c *Client) ListNotifications(ctx context.Context) ([]types.Notification, error) {
	var out []types.Notification
	return out, c.do(ctx, http.MethodGet, "/notifications", nil, &out)
}

// AckNotification marks a notification delivered (handled) so it isn't re-shown.
func (c *Client) AckNotification(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/notifications/%d/ack", id), nil, nil)
}

// ListCron returns the user's cron jobs.
func (c *Client) ListCron(ctx context.Context) ([]types.CronJob, error) {
	var out []types.CronJob
	return out, c.do(ctx, http.MethodGet, "/cron", nil, &out)
}

// CreateCron creates a cron job.
func (c *Client) CreateCron(ctx context.Context, req types.CreateCronJobRequest) (*types.CronJob, error) {
	var out types.CronJob
	return &out, c.do(ctx, http.MethodPost, "/cron", req, &out)
}

// GetCron returns one cron job.
func (c *Client) GetCron(ctx context.Context, id int64) (*types.CronJob, error) {
	var out types.CronJob
	return &out, c.do(ctx, http.MethodGet, fmt.Sprintf("/cron/%d", id), nil, &out)
}

// UpdateCron applies a partial update (enable/disable/edit).
func (c *Client) UpdateCron(ctx context.Context, id int64, req types.UpdateCronJobRequest) (*types.CronJob, error) {
	var out types.CronJob
	return &out, c.do(ctx, http.MethodPatch, fmt.Sprintf("/cron/%d", id), req, &out)
}

// DeleteCron removes a cron job.
func (c *Client) DeleteCron(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/cron/%d", id), nil, nil)
}

// RunCron dispatches a cron job immediately.
func (c *Client) RunCron(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/cron/%d/run", id), nil, nil)
}

// GetConfig returns the user's per-user config (key -> value).
func (c *Client) GetConfig(ctx context.Context) (map[string]string, error) {
	var out map[string]string
	return out, c.do(ctx, http.MethodGet, "/config", nil, &out)
}

// SetConfig upserts one config key.
func (c *Client) SetConfig(ctx context.Context, key, value string) error {
	return c.do(ctx, http.MethodPut, "/config/"+url.PathEscape(key), types.SetConfigRequest{Value: value}, nil)
}

// DeleteConfig removes one config key.
func (c *Client) DeleteConfig(ctx context.Context, key string) error {
	return c.do(ctx, http.MethodDelete, "/config/"+url.PathEscape(key), nil, nil)
}

// Messages returns a session's messages.
func (c *Client) Messages(ctx context.Context, id int64) ([]types.Message, error) {
	var out []types.Message
	return out, c.do(ctx, http.MethodGet, fmt.Sprintf("/sessions/%d/messages", id), nil, &out)
}

// ListSkills returns the skill catalogue.
func (c *Client) ListSkills(ctx context.Context) ([]types.SkillInfo, error) {
	var out []types.SkillInfo
	return out, c.do(ctx, http.MethodGet, "/skills", nil, &out)
}

// GetSkill downloads a skill's files.
func (c *Client) GetSkill(ctx context.Context, name string) (*types.SkillFiles, error) {
	var out types.SkillFiles
	return &out, c.do(ctx, http.MethodGet, "/skills/"+name, nil, &out)
}

// PutSkill uploads a user's override.
func (c *Client) PutSkill(ctx context.Context, name string, files map[string]string) error {
	return c.do(ctx, http.MethodPut, "/skills/"+name, types.SkillFiles{Name: name, Files: files}, nil)
}

// ResetSkill deletes a user's override.
func (c *Client) ResetSkill(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/skills/"+name, nil, nil)
}

// ListMemory returns memories visible to the current user.
func (c *Client) ListMemory(ctx context.Context, scope string) ([]types.Memory, error) {
	var out []types.Memory
	path := "/memory"
	if scope != "" {
		path += "?scope=" + scope
	}
	return out, c.do(ctx, http.MethodGet, path, nil, &out)
}

// FindMemory returns full memory records matching the query (ranked best-first,
// across the user's own and shared memories), shaped like a memory listing.
func (c *Client) FindMemory(ctx context.Context, query string) ([]types.Memory, error) {
	var out []types.Memory
	return out, c.do(ctx, http.MethodGet, "/memory/find?q="+url.QueryEscape(query), nil, &out)
}

// GetMemory returns one memory by its composite id if visible to the current user.
func (c *Client) GetMemory(ctx context.Context, id string) (*types.Memory, error) {
	var out types.Memory
	return &out, c.do(ctx, http.MethodGet, "/memory/"+id, nil, &out)
}

// DeleteMemory deletes a user-scoped memory owned by the caller, or a shared memory if the caller is admin.
func (c *Client) DeleteMemory(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/memory/"+id, nil, nil)
}

// ListMemoryConflicts returns unresolved duplicate/conflict flags for the user.
func (c *Client) ListMemoryConflicts(ctx context.Context) ([]types.MemoryConflict, error) {
	var out []types.MemoryConflict
	return out, c.do(ctx, http.MethodGet, "/memory/conflicts", nil, &out)
}

// ResolveMemoryConflict marks a conflict as reviewed/resolved.
func (c *Client) ResolveMemoryConflict(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/memory/conflicts/"+id+"/resolve", nil, nil)
}

// SearchDocuments searches the org RAG corpus.
func (c *Client) SearchDocuments(ctx context.Context, q string) ([]types.SearchResult, error) {
	var out []types.SearchResult
	return out, c.do(ctx, http.MethodGet, "/documents/search?q="+q, nil, &out)
}

// UploadDocument uploads a local file (e.g. a PDF) to the org RAG corpus; the
// server extracts its text (PDFs via PDFium) and ingests it. Uses a generous
// timeout since server-side extraction + embedding can take a while.
func (c *Client) UploadDocument(ctx context.Context, path, title string) (*types.Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if title != "" {
		_ = mw.WriteField("title", title)
	}
	fw, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/documents", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.iface != "" {
		req.Header.Set(types.HeaderInterface, c.iface)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var er types.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&er)
		if er.Error == "" {
			er.Error = resp.Status
		}
		return nil, fmt.Errorf("%s", er.Error)
	}
	var d types.Document
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Usage returns usage records for the current user.
func (c *Client) Usage(ctx context.Context) ([]types.UsageRecord, error) {
	var out []types.UsageRecord
	return out, c.do(ctx, http.MethodGet, "/usage", nil, &out)
}

// Session is a live WebSocket connection to a server-side session. The server
// runs the turn independently of this connection, so dropping it does not cancel
// the turn — reopen the Session (with HaveSeq) to resume. Events are delivered on
// Events() until the socket closes.
type Session struct {
	c       *websocket.Conn
	events  chan types.StreamEvent
	cancel  context.CancelFunc
	writeMu sync.Mutex
}

// OpenSession dials the session WebSocket and performs the resume handshake.
// haveSeq is the highest StreamEvent.Seq already processed by the caller (0 for a
// cold resume: load committed history via Messages, then the server replays any
// in-flight turn). The returned Session streams events on Events().
func (c *Client) OpenSession(ctx context.Context, sessionID int64, haveSeq int) (*Session, error) {
	wsURL := wsScheme(c.baseURL) + fmt.Sprintf("/api/v1/sessions/%d/ws", sessionID)
	hdr := http.Header{}
	if c.token != "" {
		hdr.Set("Authorization", "Bearer "+c.token)
	}
	if c.iface != "" {
		hdr.Set(types.HeaderInterface, c.iface)
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader:   hdr,
		Subprotocols: []string{"harlequin"},
	})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(8 << 20) // tool outputs / long messages
	// The hello handshake announces our resume position.
	if err := wsjson.Write(ctx, conn, types.WSClientMessage{Type: types.WSClientHello, HaveSeq: haveSeq}); err != nil {
		conn.CloseNow()
		return nil, err
	}

	rctx, cancel := context.WithCancel(context.Background())
	s := &Session{c: conn, events: make(chan types.StreamEvent, 256), cancel: cancel}
	go s.readLoop(rctx)
	return s, nil
}

func (s *Session) readLoop(ctx context.Context) {
	defer close(s.events)
	for {
		var ev types.StreamEvent
		if err := wsjson.Read(ctx, s.c, &ev); err != nil {
			return
		}
		select {
		case s.events <- ev:
		case <-ctx.Done():
			return
		}
	}
}

// Events is the stream of server events; it is closed when the socket ends.
func (s *Session) Events() <-chan types.StreamEvent { return s.events }

// Submit sends a prompt to the live session (queued server-side if a turn runs).
func (s *Session) Submit(content string) error {
	return s.write(types.WSClientMessage{Type: types.WSClientPrompt, Content: content})
}

// Interrupt cancels the in-flight turn without ending the session.
func (s *Session) Interrupt() error {
	return s.write(types.WSClientMessage{Type: types.WSClientInterrupt})
}

func (s *Session) write(m types.WSClientMessage) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return wsjson.Write(ctx, s.c, m)
}

// Close tears down the connection (the server-side session keeps running).
func (s *Session) Close() error {
	s.cancel()
	return s.c.Close(websocket.StatusNormalClosure, "")
}

// wsScheme rewrites an http(s) base URL to its ws(s) equivalent.
func wsScheme(base string) string {
	if strings.HasPrefix(base, "https://") {
		return "wss://" + strings.TrimPrefix(base, "https://")
	}
	if strings.HasPrefix(base, "http://") {
		return "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base
}
