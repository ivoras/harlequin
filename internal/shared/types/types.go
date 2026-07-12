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

// Session is a chat session, tied to a single interface/API.
type Session struct {
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
	// ProjectID is set when the session belongs to a project (it then lives in the
	// project's database and is visible to all members); nil for a personal session.
	ProjectID *int64 `json:"project_id,omitempty"`
	// OwnerEmail, populated for project sessions, is who originally created it.
	OwnerEmail string `json:"owner_email,omitempty"`
}

// SetConfigRequest is the body of PUT /config/{key}.
type SetConfigRequest struct {
	Value string `json:"value"`
}

// Hat is an org-defined collection of resources for a type of work: an optional
// system prompt (empty = use the default) plus a visible-skills list (which
// skills are available while the hat is worn; empty = all). Hats live in the
// shared database (system_prompt.md + optional "skills/<name>/..." overrides).
type Hat struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Skills       []string `json:"skills,omitempty"`
	// OverlaySkills are the skills this hat carries its own variants of
	// (files under skills/<name>/...); they take precedence over normal
	// project/shared/user resolution while the hat is worn.
	OverlaySkills []string `json:"overlay_skills,omitempty"`
	// HasCustomPrompt: system_prompt.md has a non-empty body. PromptDisabled:
	// that body is kept but inactive (use_prompt: false), so the default
	// system prompt is used while the hat is worn.
	HasCustomPrompt bool `json:"has_custom_prompt,omitempty"`
	PromptDisabled  bool `json:"prompt_disabled,omitempty"`
}

// SetHatPromptRequest is the body of POST /hats/{name}/prompt.
type SetHatPromptRequest struct {
	Enabled bool `json:"enabled"`
}

// CreateHatRequest is the body of POST /hats.
type CreateHatRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// AddHatSkillRequest is the body of POST /hats/{name}/skills.
type AddHatSkillRequest struct {
	Skill string `json:"skill"`
}

// HatFiles is the body of PUT /hats/{name}: the hat's complete file set
// (relpath -> content; system_prompt.md plus optional skill overrides).
type HatFiles struct {
	Name  string            `json:"name"`
	Files map[string]string `json:"files"`
}

// SetSessionHatRequest is the body of POST /sessions/{id}/hat.
type SetSessionHatRequest struct {
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
// title changed (e.g. by the auto-titler): re-read/apply it for SessionID.
// The new title is carried in Title. Not shown as a chat message.
const NotifyKindSessionTitle = "session-title"

// NotifyKindAlert is an admin/owner broadcast alert sent to every user (the
// /alert command). Shown in the client alert box like any passive notification.
const NotifyKindAlert = "alert"

// BroadcastAlertRequest is the body of POST /alerts (owner/admin only): a text
// message delivered as an alert to all users.
type BroadcastAlertRequest struct {
	Message string `json:"message"`
}

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
	// SessionID targets a specific session (set for control kinds like
	// session-title); nil for general notifications.
	SessionID *int64 `json:"session_id,omitempty"`
	// Interface targets a specific interface (e.g. "TUI"): only clients announcing
	// it receive the notification. Empty = broadcast to any interface.
	Interface string `json:"interface,omitempty"`
}

// Project is a shared workspace: a collection of members, sessions, documents,
// memories, and a chatroom, each project backed by its own database.
type Project struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedBy int64     `json:"created_by"`
	// CreatedByEmail is the creator's email (list/detail responses), so clients
	// can disambiguate projects with duplicate names.
	CreatedByEmail string    `json:"created_by_email,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	// Members is populated on the single-project detail endpoint.
	Members []ProjectMember `json:"members,omitempty"`
}

// ProjectMember is a user belonging to a project.
type ProjectMember struct {
	UserID   int64     `json:"user_id"`
	Email    string    `json:"email"`
	JoinedAt time.Time `json:"joined_at"`
}

// ProjectInvite is a pending invitation for a user to join a project.
type ProjectInvite struct {
	ID          int64     `json:"id"`
	ProjectID   int64     `json:"project_id"`
	ProjectName string    `json:"project_name"`
	InvitedBy   string    `json:"invited_by"` // inviter's email
	Status      string    `json:"status"`     // pending | accepted | declined
	CreatedAt   time.Time `json:"created_at"`
}

// ChatMessage is one message in a project's chatroom.
type ChatMessage struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Email     string    `json:"email"` // author's email (resolved for display)
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateProjectRequest is the body of POST /projects.
type CreateProjectRequest struct {
	Name string `json:"name"`
}

// InviteRequest is the body of POST /projects/{id}/invite: invite by email.
type InviteRequest struct {
	Email string `json:"email"`
}

// Cron job kinds.
const (
	CronKindJS    = "js"    // run a JavaScript script (LLM-free)
	CronKindSkill = "skill" // run an agent turn that can use a skill
)

// CronNoUpdateSentinel is the marker a cron run emits to declare "nothing new to
// report" — the scheduler treats it (and empty output) as a no-op: no
// notification, and the previous meaningful output is kept as the change
// baseline. JS jobs can return ""/null; skill jobs are told to reply with this.
const CronNoUpdateSentinel = "NO_UPDATE"

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
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
	LastStatus    string     `json:"last_status,omitempty"` // "ok" | "error"
	LastOutput    string     `json:"last_output,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
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
	Name          *string `json:"name,omitempty"`
	Spec          *string `json:"spec,omitempty"`
	Kind          *string `json:"kind,omitempty"`
	Target        *string `json:"target,omitempty"`
	Prompt        *string `json:"prompt,omitempty"`
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

// Message is a single message in a session.
type Message struct {
	ID        int64      `json:"id"`
	SessionID int64      `json:"session_id"`
	Role      string     `json:"role"` // system | user | assistant | tool
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
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

// CreateSessionRequest is the body of POST /sessions.
type CreateSessionRequest struct {
	Title string `json:"title"`
	Hat   string `json:"hat,omitempty"` // optional: wear this hat from the start
}

// SendMessageRequest is the prompt a client submits over the session WebSocket
// (the `content` of a WSClientPrompt frame).
type SendMessageRequest struct {
	Content string `json:"content"`
}

// Server→client stream event types (carried in StreamEvent.Type) sent over the
// session WebSocket. Streaming is WebSocket-only; the historical SSE prefix is
// retained on the constants for continuity, but the wire values are stable.
const (
	SSEToken          = "token"
	SSEThinking       = "thinking"
	SSEToolCall       = "tool_call"
	SSEToolResult     = "tool_result"
	SSEError          = "error"
	SSEAskUser        = "ask_user"
	SSEPromptProgress = "prompt_progress" // llama.cpp prefill progress, before first token
	SSEDone           = "done"
	// SSEUserMessage echoes the user's prompt as the first event of a turn, so a
	// (re)connecting client renders it from the stream rather than optimistically
	// — making resume a single source of truth.
	SSEUserMessage = "user_message"
	// SSESynced is the control frame the server sends right after a client's hello,
	// describing the live session state before replay/live events begin.
	SSESynced = "synced"
	// SSENotification pushes a server→user notification to a connected client
	// (replacing client-side polling). The payload is in StreamEvent.Notification.
	SSENotification = "notification"
	// SSEChat is a project chatroom message broadcast to connected members
	// (payload in StreamEvent.Chat).
	SSEChat = "chat"
)

// WebSocket client→server frame types (WSClientMessage.Type).
const (
	WSClientHello     = "hello"     // first frame: announce last seen seq for resume
	WSClientPrompt    = "prompt"    // submit a prompt (Content) to the live session
	WSClientInterrupt = "interrupt" // cancel the in-flight turn (keep the session alive)
	WSClientChat      = "chat"      // post a message (Content) to a project chatroom
)

// WSClientMessage is a frame sent by a client to the server over the session
// WebSocket.
type WSClientMessage struct {
	Type string `json:"type"`
	// Content is the prompt text (WSClientPrompt).
	Content string `json:"content,omitempty"`
	// HaveSeq is the highest StreamEvent.Seq the client has already processed
	// (WSClientHello): 0 = cold resume (load committed history + replay the
	// in-flight turn), >0 = warm reconnect (replay only the tail seq > HaveSeq).
	HaveSeq int `json:"have_seq,omitempty"`
}

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
	// Seq is the session-monotonic sequence number of this event, assigned by the
	// session hub. Clients track the highest Seq they have processed and send it
	// as HaveSeq on reconnect to replay only what they missed.
	Seq int `json:"seq,omitempty"`
	// Running (SSESynced) reports whether a turn is in flight on the server.
	Running bool `json:"running,omitempty"`
	// CommittedThrough (SSESynced) is the highest message id already durably
	// committed before the in-flight turn began. On a cold resume the client
	// renders committed messages with id <= CommittedThrough and reconstructs the
	// in-flight turn from the replayed buffer (which re-emits it from its first
	// event, including the SSEUserMessage echo).
	CommittedThrough int64 `json:"committed_through,omitempty"`
	// Notification (SSENotification) is a server→user notification pushed to the
	// connected client.
	Notification *Notification `json:"notification,omitempty"`
	// Chat (SSEChat) is a project chatroom message.
	Chat *ChatMessage `json:"chat,omitempty"`
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

// ContextCategory is one line item in a ContextBreakdown, e.g. "System prompt"
// or "Messages".
type ContextCategory struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
}

// ContextBreakdown estimates how a session's next request would fill the
// model's context window, broken down by category. Token counts are estimates
// (see llm.EstimateTextTokens) unless the session has completed a turn, in
// which case Total/ContextMax come from the provider-reported usage instead.
type ContextBreakdown struct {
	Model      string            `json:"model"`
	ContextMax int               `json:"context_max"`
	Total      int               `json:"total"`
	Categories []ContextCategory `json:"categories"`
}

// SkillInfo describes a skill in a listing.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // "project" | "shared" | "user" | "hat"
	// AlsoIn lists further scopes holding a copy of this skill that the Source
	// scope shadows (resolution order), so clients can surface "your edit in
	// scope X is invisible" situations.
	AlsoIn []string `json:"also_in,omitempty"`
}

// SkillFiles is a map of relative path -> file contents. Scope selects which
// scope a write targets ("user" | "shared" | "project"); on reads it reports the
// scope the skill resolved from. Empty means the default (user, or project when
// in a project session).
type SkillFiles struct {
	Name  string            `json:"name"`
	Scope string            `json:"scope,omitempty"`
	Files map[string]string `json:"files"`
}

// MemorySlot is one normalized (key, value) attribute attached to a memory. A
// memory may carry several slots — e.g. the same date fact filed under both
// user.birthday and memory.date.
type MemorySlot struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

// Memory is a stored memory entry.
type Memory struct {
	ID        string       `json:"id"`    // composite: "u.<localid>" | "s.<localid>"
	Scope     string       `json:"scope"` // "user" | "shared"
	UserID    *int64       `json:"user_id,omitempty"`
	Content   string       `json:"content"`
	Slots     []MemorySlot `json:"slots,omitempty"` // normalized attribute slots, if extracted
	Source    string       `json:"source,omitempty"`
	Pinned    bool         `json:"pinned"`
	ExpiresAt *time.Time   `json:"expires_at,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
}

// CreateMemoryRequest is the body of POST /memory.
type CreateMemoryRequest struct {
	Scope     string     `json:"scope"`
	Content   string     `json:"content"`
	Source    string     `json:"source,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// ProjectID targets a project's memory when Scope is "project" (members only).
	ProjectID int64 `json:"project_id,omitempty"`
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
	// Scope is the corpus the document lives in: "personal", "shared", or
	// "project". Needed to address it for delete across corpora.
	Scope string `json:"scope,omitempty"`
	// Chunks is the number of RAG chunks (vector records) produced on ingest;
	// surfaced so the client can report processing status.
	Chunks int `json:"chunks,omitempty"`
	// OriginalName is the uploaded file's original (UTF-8) name; StoredPath is
	// its on-disk path (7-bit ASCII transliteration) relative to the scope's
	// files/ directory. Empty for documents ingested as raw text (no file).
	OriginalName string `json:"original_name,omitempty"`
	StoredPath   string `json:"stored_path,omitempty"`
	// Description is a short LLM-generated catalogue line (what the document
	// is, its subject, version hints) used to resolve paraphrased references.
	Description string `json:"description,omitempty"`
}

// CreateDocumentRequest is the body of POST /documents.
type CreateDocumentRequest struct {
	Title   string `json:"title"`
	URI     string `json:"uri"`
	Mime    string `json:"mime"`
	Content string `json:"content"`
	// Scope selects the corpus to ingest into: "shared" (default, org-wide),
	// "personal" (the user's own docs), or "project" (requires ProjectID).
	Scope     string `json:"scope,omitempty"`
	ProjectID int64  `json:"project_id,omitempty"`
	// OriginalName is set server-side from an uploaded file's name (the stored
	// original filename). Ignored on JSON (raw-text) ingests.
	OriginalName string `json:"-"`
	// PageStarts are the rune offsets at which each page of Content begins
	// (set server-side for PDFs), used to assign a page to each chunk.
	PageStarts []int `json:"-"`
	// Description is the catalogue line (generated server-side at upload when
	// empty; may be supplied explicitly on raw-text ingests).
	Description string `json:"description,omitempty"`
}

// SearchResult is a hybrid-search hit. ID is a composite id encoding the scope:
// memories use "u.<n>" (personal) / "s.<n>" (shared) / "p.<n>" (project);
// document chunks use "d.u.<n>" / "d.s.<n>" / "d.p.<n>". The Scope field carries
// the same information as a plain label.
type SearchResult struct {
	ID       string   `json:"id"`
	Content  string   `json:"content"`
	SlotKeys []string `json:"slot_keys,omitempty"`
	Score    float64  `json:"score"`
	// Scope reports where the result was found: "personal" (the user's own
	// data), "shared" (organisation-wide), or "project" (the active project).
	Scope string `json:"scope,omitempty"`
	// Source describes where a document hit came from ("<title> · chunk <n>");
	// empty for memory results.
	Source string `json:"source,omitempty"`
	// Citation metadata (document hits only): the owning document, the 1-based
	// page the chunk starts on (0 = unpaged), its mime, and whether the original
	// file is stored (servable via GET /documents/{id}/file).
	DocumentID int64  `json:"document_id,omitempty"`
	Page       int    `json:"page,omitempty"`
	Mime       string `json:"mime,omitempty"`
	HasFile    bool   `json:"has_file,omitempty"`
}

// DocChunkInfo resolves a chunk citation (d.u.N / d.s.N / d.p.N) for clients:
// which document it belongs to, where in it, and whether the original file can
// be opened.
type DocChunkInfo struct {
	ID         string `json:"id"`
	Scope      string `json:"scope"`
	DocumentID int64  `json:"document_id"`
	Title      string `json:"title"`
	Mime       string `json:"mime"`
	Page       int    `json:"page,omitempty"`
	HasFile    bool   `json:"has_file"`
	// ProjectID identifies the project a project-scoped chunk lives in (from a
	// qualified d.p<id>.N citation, or the request's project parameter), so the
	// client can open the document file against the right corpus.
	ProjectID int64 `json:"project_id,omitempty"`
}

// IngestJobStatus is the pollable state of an asynchronous document ingestion
// (GET /documents/jobs/{id}). Stage is "starting" / "extracting" /
// "describing" / "embedding" / "done"; Done/Total are embedded-chunk counts
// during the embedding stage (0 when unknown). Document is set once Finished
// without Error.
type IngestJobStatus struct {
	ID       string    `json:"id"`
	Stage    string    `json:"stage"`
	Done     int       `json:"done,omitempty"`
	Total    int       `json:"total,omitempty"`
	Finished bool      `json:"finished"`
	Error    string    `json:"error,omitempty"`
	Document *Document `json:"document,omitempty"`
}

// UsageRecord is a per-completion accounting row.
type UsageRecord struct {
	ID               int64     `json:"id"`
	UserID           int64     `json:"user_id"`
	SessionID        *int64    `json:"session_id,omitempty"`
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
