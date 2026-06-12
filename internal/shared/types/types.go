// Package types holds the REST DTOs shared by the Harlequin client and server.
package types

import "time"

// Role names, ordered from highest privilege to lowest. "owner" is the only
// role that may create or edit users; "owner" and "admin" may create and delete
// shared memories (and other org-wide actions); "user" is an ordinary account.
const (
	RoleOwner = "owner"
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// IsOwner reports whether the role may manage users (owner only).
func IsOwner(role string) bool { return role == RoleOwner }

// IsElevated reports whether the role has org-wide administrative privileges
// (owner or admin): creating/deleting shared memories, deleting documents,
// reading the audit log, publishing skills, and viewing other users' data.
func IsElevated(role string) bool { return role == RoleOwner || role == RoleAdmin }

// User is a public representation of an account (no password hash). The login
// identity is the email address.
type User struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"` // "owner" | "admin" | "user"
	CreatedAt time.Time `json:"created_at"`
}

// LoginRequest is the body of POST /auth/login.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse is returned on a successful login.
type LoginResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

// CreateUserRequest is the body of POST /users (owner only).
type CreateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// RegisterRequest is the body of POST /auth/register: it starts self-registration
// by emailing a verification magic code.
type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// RegisterResponse acknowledges that a verification code was sent.
type RegisterResponse struct {
	Status string `json:"status"` // "verification_sent"
	Email  string `json:"email"`
}

// VerifyRequest is the body of POST /auth/verify: it completes registration by
// presenting the emailed magic code, returning a login token on success.
type VerifyRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

// RegistrationStatus is returned by GET /auth/registration so clients can show or
// hide the self-registration UI.
type RegistrationStatus struct {
	Enabled bool `json:"enabled"`
}

// HeaderInterface is the request header by which a REST client announces which
// interface it is (e.g. "TUI"). The transport ("API") is server-derived.
const HeaderInterface = "X-Harlequin-Interface"

// Interface is the medium through which a user talks to the agent. Each session
// is tied to exactly one. The transport an interface uses is its API (below).
const (
	InterfaceTUI      = "TUI"
	InterfaceWeb      = "Web" // the browser SPA
	InterfaceTelegram = "Telegram"
	InterfaceCron     = "Cron" // internal: scheduled jobs that start an agent turn
)

// API is the transport a client reaches the server through.
const (
	APIREST     = "REST"
	APITelegram = "Telegram"
	APICron     = "Cron"
)

// Conversation is a chat session, tied to a single interface/API.
type Conversation struct {
	ID     int64   `json:"id"`
	UserID int64   `json:"user_id"`
	Title  string  `json:"title"`
	Hat    *string `json:"hat,omitempty"` // the worn hat's name, or nil
	// API is the transport the session arrived over ("REST"); Interface is the
	// medium ("TUI"). Set at creation and fixed for the session's lifetime.
	API       string    `json:"api"`
	Interface string    `json:"interface"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SetConfigRequest is the body of PUT /config/{key}.
type SetConfigRequest struct {
	Value string `json:"value"`
}

// Hat is an org-defined, file-based collection of resources for a type of work:
// an optional system prompt (empty = use the default) plus a visible-skills list
// (which skills are available while the hat is worn; empty = all). Hats live as
// directories under data/hats/<name>/ (system_prompt.md + optional skills/
// overrides).
type Hat struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Skills       []string `json:"skills,omitempty"`
}

// SetConversationHatRequest is the body of POST /conversations/{id}/hat.
type SetConversationHatRequest struct {
	Hat string `json:"hat"` // empty clears the hat
}

// MCPServer describes a registered MCP server as seen by clients. Secret fields
// (header value, OAuth client secret, tokens) are never serialized; HasCredential
// reports whether a static credential is stored.
type MCPServer struct {
	Scope         string   `json:"scope"` // "shared" | "user"
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	Transport     string   `json:"transport"`
	AuthType      string   `json:"auth_type"` // "none" | "header" | "oauth"
	HeaderNames   []string `json:"header_names,omitempty"`
	HasCredential bool     `json:"has_credential"`
	Enabled       bool     `json:"enabled"`
	// Status, populated on list/get.
	AuthSatisfied bool   `json:"auth_satisfied"`
	NeedsAuth     bool   `json:"needs_auth"`
	ToolCount     int    `json:"tool_count,omitempty"`
	Error         string `json:"error,omitempty"`
	// Tools is populated on the single-server detail endpoint (GET /mcp/{scope}/{name}).
	Tools []MCPTool `json:"tools,omitempty"`
}

// NotifyKindSessionTitle is a control notification telling the client a session's
// title changed (e.g. by the auto-titler): re-read/apply it for ConversationID.
// The new title is carried in Title. Not shown as a chat message.
const NotifyKindSessionTitle = "session-title"

// Notification is a server→user message stored in the user's database. It may
// carry a prompt the client can run, optionally automatically (AutoRun).
type Notification struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	AutoRun     bool   `json:"auto_run"`
	Status      string `json:"status"`
	// ConversationID targets a specific session (set for control kinds like
	// session-title); nil for general notifications.
	ConversationID *int64 `json:"conversation_id,omitempty"`
	// Interface targets a specific interface (e.g. "TUI"): only clients announcing
	// it receive the notification. Empty = broadcast to any interface.
	Interface string `json:"interface,omitempty"`
}

// Cron job kinds.
const (
	CronKindJS    = "js"    // run a JavaScript script (LLM-free)
	CronKindSkill = "skill" // run an agent turn that can use a skill
)

// CronJob is a per-user scheduled task: a JS script or an agent/skill turn run on
// a cron schedule with user-provided inputs.
type CronJob struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Spec is the cron schedule (5-field, @descriptor, or "@every <dur>").
	Spec string `json:"spec"`
	Kind string `json:"kind"` // CronKindJS | CronKindSkill
	// Target is the script for a JS job (an inline body or a skill:// / storage://
	// / tmp:// URI), or the skill name for a skill job.
	Target string `json:"target"`
	// Prompt is the message sent to the agent for a skill job.
	Prompt string `json:"prompt,omitempty"`
	// Input is a JSON object of inputs: exposed to a JS job as the global `args`.
	Input   string `json:"input,omitempty"`
	Enabled bool   `json:"enabled"`
	// Notify creates a user notification when the job's output changes.
	Notify bool `json:"notify"`
	// NotifyChannel is where that notification is delivered: "inapp" (default),
	// "email", or "telegram".
	NotifyChannel string     `json:"notify_channel,omitempty"`
	NextRunAt     *time.Time `json:"next_run_at,omitempty"`
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	LastStatus string     `json:"last_status,omitempty"` // "ok" | "error"
	LastOutput string     `json:"last_output,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// CreateCronJobRequest is the body of POST /cron (and the cron_create tool).
type CreateCronJobRequest struct {
	Name    string `json:"name"`
	Spec    string `json:"spec"`
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Prompt  string `json:"prompt,omitempty"`
	Input   string `json:"input,omitempty"`
	Notify  *bool  `json:"notify,omitempty"`  // default true
	Enabled *bool  `json:"enabled,omitempty"` // default true
	// NotifyChannel is the delivery channel: "inapp" (default), "email", "telegram".
	NotifyChannel string `json:"notify_channel,omitempty"`
}

// UpdateCronJobRequest is the body of PATCH /cron/{id}; nil fields are unchanged.
type UpdateCronJobRequest struct {
	Name    *string `json:"name,omitempty"`
	Spec    *string `json:"spec,omitempty"`
	Kind    *string `json:"kind,omitempty"`
	Target  *string `json:"target,omitempty"`
	Prompt  *string `json:"prompt,omitempty"`
	Input         *string `json:"input,omitempty"`
	Notify        *bool   `json:"notify,omitempty"`
	Enabled       *bool   `json:"enabled,omitempty"`
	NotifyChannel *string `json:"notify_channel,omitempty"`
}

// MCPTool is a tool advertised by an MCP server.
type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// MCPHeader is one static request header for header auth.
type MCPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// RegisterMCPRequest is the body of POST /mcp.
type RegisterMCPRequest struct {
	Scope       string      `json:"scope"` // "shared" | "user" (default "user")
	Name        string      `json:"name"`
	URL         string      `json:"url"`
	AuthType    string      `json:"auth_type"` // "none" | "header" | "oauth"
	Headers     []MCPHeader `json:"headers,omitempty"`
	OAuthScopes []string    `json:"oauth_scopes,omitempty"`
	Enabled     *bool       `json:"enabled,omitempty"`
}

// MCPTestResult is the body of POST /mcp/{scope}/{name}/test.
type MCPTestResult struct {
	OK    bool     `json:"ok"`
	Tools []string `json:"tools,omitempty"`
	Error string   `json:"error,omitempty"`
}

// MCPAuthStartResult is the body of POST /mcp/{scope}/{name}/oauth/start.
type MCPAuthStartResult struct {
	AuthorizeURL string `json:"authorize_url"`
}

// Message is a single message in a conversation.
type Message struct {
	ID             int64      `json:"id"`
	ConversationID int64      `json:"conversation_id"`
	Role           string     `json:"role"` // system | user | assistant | tool
	Content        string     `json:"content"`
	ToolCalls      []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID and Name link a tool-result message back to the assistant
	// tool call it answers (required by OpenAI-compatible providers on replay).
	ToolCallID string    `json:"tool_call_id,omitempty"`
	Name       string    `json:"name,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
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
	Hat   string `json:"hat,omitempty"` // optional: wear this hat from the start
}

// SendMessageRequest is the body of POST /conversations/{id}/messages.
type SendMessageRequest struct {
	Content string `json:"content"`
}

// SSEEvent types streamed from the message endpoint.
const (
	SSEToken          = "token"
	SSEThinking       = "thinking"
	SSEToolCall       = "tool_call"
	SSEToolResult     = "tool_result"
	SSEError          = "error"
	SSEAskUser        = "ask_user"
	SSEPromptProgress = "prompt_progress" // llama.cpp prefill progress, before first token
	SSEDone           = "done"
)

// StreamEvent is a single SSE event payload (JSON-encoded in the `data:` field).
type StreamEvent struct {
	Type string `json:"type"`
	// Text is assistant response text (SSEToken).
	Text string `json:"text,omitempty"`
	// Thinking is model reasoning text (SSEThinking), distinct from the final answer.
	Thinking string `json:"thinking,omitempty"`
	// Tool call info (for SSEToolCall / SSEToolResult).
	ToolName   string `json:"tool_name,omitempty"`
	ToolArgs   string `json:"tool_args,omitempty"`
	Output     string `json:"output,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	// Error message (for SSEError).
	Error string `json:"error,omitempty"`
	// Options are suggested answers the user can choose from (for SSEAskUser).
	Options []string `json:"options,omitempty"`
	// Prompt-processing progress (SSEPromptProgress): tokens evaluated so far and
	// the total to evaluate for this prefill (cache hits already excluded).
	PromptProcessed int `json:"prompt_processed,omitempty"`
	PromptTotal     int `json:"prompt_total,omitempty"`
	// Source labels progress from a nested/delegated LLM call rather than the main
	// turn (e.g. "WebFetch"), so the client can distinguish it from the user's own
	// prompt processing. Empty means the main turn.
	Source string `json:"source,omitempty"`
	// Context reporting (SSEDone): prompt/context size and model limit for the turn.
	Model         string `json:"model,omitempty"`
	ContextTokens int    `json:"context_tokens,omitempty"`
	ContextMax    int    `json:"context_max,omitempty"`
	// Timing (SSEDone), populated only when the server's timing report is enabled.
	// Present indicates timing is available for this turn.
	Timing *TurnTiming `json:"timing,omitempty"`
}

// TurnTiming reports model operation timing aggregated over a turn's LLM calls.
type TurnTiming struct {
	PromptTokens     int     `json:"prompt_tokens"`     // tokens processed during prefill (PP)
	CompletionTokens int     `json:"completion_tokens"` // tokens generated (TG)
	PrefillMS        int64   `json:"prefill_ms"`        // total prompt-processing time
	DecodeMS         int64   `json:"decode_ms"`         // total token-generation time
	TotalMS          int64   `json:"total_ms"`          // wall-clock time for the turn
	PPRate           float64 `json:"pp_rate"`           // prompt-processing speed, tokens/sec
	TGRate           float64 `json:"tg_rate"`           // token-generation speed, tokens/sec
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
	ID        string     `json:"id"`    // composite: "u.<localid>" | "s.<localid>"
	Scope     string     `json:"scope"` // "user" | "shared"
	UserID    *int64     `json:"user_id,omitempty"`
	Content   string     `json:"content"`
	SlotKey   string     `json:"slot_key,omitempty"`   // normalized attribute key, if extracted
	SlotValue string     `json:"slot_value,omitempty"` // normalized value paired with SlotKey
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

// MemoryConflict is a flagged contradictory or duplicate memory pair. IDs are
// composite: the conflict ID and both endpoints are "u.<n>"/"s.<n>" strings.
type MemoryConflict struct {
	ID           string     `json:"id"`
	MemoryA      string     `json:"memory_a"`
	MemoryB      string     `json:"memory_b"`
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

// SearchResult is a hybrid-search hit. ID is a composite id: "u.<n>"/"s.<n>" for
// memories, "d.<n>" for document chunks.
type SearchResult struct {
	ID      string  `json:"id"`
	Content string  `json:"content"`
	SlotKey string  `json:"slot_key,omitempty"`
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
