// Package apiclient is the client-side REST + SSE client for the Harlequin server.
package apiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// Client talks to the Harlequin server.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New constructs a Client.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
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

// Login authenticates and returns the issued token.
func (c *Client) Login(ctx context.Context, username, password string) (*types.LoginResponse, error) {
	var resp types.LoginResponse
	if err := c.do(ctx, http.MethodPost, "/auth/login", types.LoginRequest{Username: username, Password: password}, &resp); err != nil {
		return nil, err
	}
	c.token = resp.Token
	return &resp, nil
}

// Me returns the current user.
func (c *Client) Me(ctx context.Context) (*types.User, error) {
	var u types.User
	return &u, c.do(ctx, http.MethodGet, "/me", nil, &u)
}

// ListConversations returns conversations (optionally filtered).
func (c *Client) ListConversations(ctx context.Context, q string) ([]types.Conversation, error) {
	var out []types.Conversation
	path := "/conversations"
	if q != "" {
		path += "?q=" + q
	}
	return out, c.do(ctx, http.MethodGet, path, nil, &out)
}

// CreateConversation starts a conversation.
func (c *Client) CreateConversation(ctx context.Context, title, hat string) (*types.Conversation, error) {
	var conv types.Conversation
	return &conv, c.do(ctx, http.MethodPost, "/conversations", types.CreateConversationRequest{Title: title, Hat: hat}, &conv)
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

// SetConversationHat sets (or clears, when hat is empty) the conversation's hat.
func (c *Client) SetConversationHat(ctx context.Context, conversationID int64, hat string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/conversations/%d/hat", conversationID),
		types.SetConversationHatRequest{Hat: hat}, nil)
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

// Messages returns a conversation's messages.
func (c *Client) Messages(ctx context.Context, id int64) ([]types.Message, error) {
	var out []types.Message
	return out, c.do(ctx, http.MethodGet, fmt.Sprintf("/conversations/%d/messages", id), nil, &out)
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

// Usage returns usage records for the current user.
func (c *Client) Usage(ctx context.Context) ([]types.UsageRecord, error) {
	var out []types.UsageRecord
	return out, c.do(ctx, http.MethodGet, "/usage", nil, &out)
}

// SendMessage streams the agent's response. Events are delivered to onEvent
// until the stream ends or ctx is cancelled.
func (c *Client) SendMessage(ctx context.Context, conversationID int64, content string, onEvent func(types.StreamEvent)) error {
	b, _ := json.Marshal(types.SendMessageRequest{Content: content})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/conversations/%d/messages", c.baseURL, conversationID), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("send: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev types.StreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		onEvent(ev)
		if ev.Type == types.SSEDone {
			break
		}
	}
	return scanner.Err()
}
