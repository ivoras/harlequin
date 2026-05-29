// Package types holds the REST DTOs shared by the Harlequin client and server.
package types

import "time"

// User is a public representation of an account (no password hash).
type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"` // "admin" | "user"
	CreatedAt time.Time `json:"created_at"`
}

// LoginRequest is the body of POST /auth/login.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginResponse is returned on a successful login.
type LoginResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

// CreateUserRequest is the body of POST /users (admin only).
type CreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// Conversation is a chat session.
type Conversation struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Message is a single message in a conversation.
type Message struct {
	ID             int64      `json:"id"`
	ConversationID int64      `json:"conversation_id"`
	Role           string     `json:"role"` // system | user | assistant | tool
	Content        string     `json:"content"`
	ToolCalls      []ToolCall `json:"tool_calls,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// ToolCall is an OpenAI-style tool call emitted by the model.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON string of arguments
}

// CreateConversationRequest is the body of POST /conversations.
type CreateConversationRequest struct {
	Title string `json:"title"`
}

// SendMessageRequest is the body of POST /conversations/{id}/messages.
type SendMessageRequest struct {
	Content string `json:"content"`
}

// SSEEvent types streamed from the message endpoint.
const (
	SSEToken      = "token"
	SSEThinking   = "thinking"
	SSEToolCall   = "tool_call"
	SSEToolResult = "tool_result"
	SSEError      = "error"
	SSEAskUser    = "ask_user"
	SSEDone       = "done"
)

// StreamEvent is a single SSE event payload (JSON-encoded in the `data:` field).
type StreamEvent struct {
	Type string `json:"type"`
	// Text is assistant response text (SSEToken).
	Text string `json:"text,omitempty"`
	// Thinking is model reasoning text (SSEThinking), distinct from the final answer.
	Thinking string `json:"thinking,omitempty"`
	// Tool call info (for SSEToolCall / SSEToolResult).
	ToolName string `json:"tool_name,omitempty"`
	ToolArgs string `json:"tool_args,omitempty"`
	Output   string `json:"output,omitempty"`
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Error message (for SSEError).
	Error string `json:"error,omitempty"`
	// Options are suggested answers the user can choose from (for SSEAskUser).
	Options []string `json:"options,omitempty"`
}

// SkillInfo describes a skill in a listing.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // "deployed" | "override" | "org"
}

// SkillFiles is a map of relative path -> file contents.
type SkillFiles struct {
	Name  string            `json:"name"`
	Files map[string]string `json:"files"`
}

// Memory is a stored memory entry.
type Memory struct {
	ID        int64      `json:"id"`
	Scope     string     `json:"scope"` // "user" | "shared"
	UserID    *int64     `json:"user_id,omitempty"`
	Content   string     `json:"content"`
	Source    string     `json:"source,omitempty"`
	Pinned    bool       `json:"pinned"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// CreateMemoryRequest is the body of POST /memory.
type CreateMemoryRequest struct {
	Scope     string     `json:"scope"`
	Content   string     `json:"content"`
	Source    string     `json:"source,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// UpdateMemoryRequest is the body of PATCH /memory/{id}.
type UpdateMemoryRequest struct {
	Pinned *bool `json:"pinned,omitempty"`
}

// MemoryConflict is a flagged contradictory or duplicate memory pair.
type MemoryConflict struct {
	ID           int64      `json:"id"`
	MemoryA      int64      `json:"memory_a"`
	MemoryB      int64      `json:"memory_b"`
	ContentA     string     `json:"content_a"`
	ContentB     string     `json:"content_b"`
	Relationship string     `json:"relationship"` // "conflicts" | "duplicate"
	Reason       string     `json:"reason"`
	Confidence   int        `json:"confidence"`
	DetectedAt   time.Time  `json:"detected_at"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
}

// Document is an org RAG document.
type Document struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	URI       string    `json:"uri"`
	Mime      string    `json:"mime"`
	CreatedBy int64     `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateDocumentRequest is the body of POST /documents.
type CreateDocumentRequest struct {
	Title   string `json:"title"`
	URI     string `json:"uri"`
	Mime    string `json:"mime"`
	Content string `json:"content"`
}

// SearchResult is a hybrid-search hit.
type SearchResult struct {
	ID      int64   `json:"id"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// UsageRecord is a per-completion accounting row.
type UsageRecord struct {
	ID               int64     `json:"id"`
	UserID           int64     `json:"user_id"`
	ConversationID   *int64    `json:"conversation_id,omitempty"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	EstCostUSD       float64   `json:"est_cost_usd"`
	CreatedAt        time.Time `json:"created_at"`
}

// AuditEntry is a coarse security/audit event.
type AuditEntry struct {
	ID        int64     `json:"id"`
	UserID    *int64    `json:"user_id,omitempty"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// PublishSkillRequest is the body of POST /skills/{name}/publish.
type PublishSkillRequest struct {
	FromUserID *int64 `json:"from_user_id,omitempty"`
}

// ErrorResponse is the standard error body.
type ErrorResponse struct {
	Error string `json:"error"`
}
