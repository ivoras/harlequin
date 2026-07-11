// TypeScript mirrors of the server DTOs (internal/shared/types/types.go). Times
// are ISO strings as serialized by Go's time.Time.

export const INTERFACE = "Web"; // X-Harlequin-Interface announced by this client

export interface User {
  id: number;
  email: string;
  role: string; // "owner" | "admin" | "user"
  created_at: string;
}

export function isElevated(role: string | undefined): boolean {
  return role === "owner" || role === "admin";
}
export function isOwner(role: string | undefined): boolean {
  return role === "owner";
}

export interface LoginResponse {
  token: string;
  user: User;
}

export interface Session {
  id: number;
  user_id: number;
  title: string;
  hat?: string;
  api: string;
  interface: string;
  created_at: string;
  updated_at: string;
  project_id?: number;
  owner_email?: string;
}

export interface Project {
  id: number;
  name: string;
  created_by: number;
  created_at: string;
  members?: ProjectMember[];
}
export interface ProjectMember {
  user_id: number;
  email: string;
  joined_at: string;
}
export interface ProjectInvite {
  id: number;
  project_id: number;
  project_name: string;
  invited_by: string;
  status: string;
  created_at: string;
}
export interface ChatMessage {
  id: number;
  user_id: number;
  email: string;
  content: string;
  created_at: string;
}

export interface ToolCall {
  id: string;
  name: string;
  arguments: string; // raw JSON string
}

export interface Message {
  id: number;
  session_id: number;
  role: string; // system | user | assistant | tool
  content: string;
  tool_calls?: ToolCall[];
  tool_call_id?: string;
  name?: string;
  created_at: string;
}

// --- streaming (WebSocket) ---
// Server→client event types (StreamEvent.type).
export const SSE = {
  Token: "token",
  Thinking: "thinking",
  ToolCall: "tool_call",
  ToolResult: "tool_result",
  Error: "error",
  AskUser: "ask_user",
  PromptProgress: "prompt_progress",
  Done: "done",
  UserMessage: "user_message",
  Synced: "synced",
  Notification: "notification",
  Chat: "chat",
} as const;

// Client→server frame types (WSClientMessage.type).
export const WS = {
  Hello: "hello",
  Prompt: "prompt",
  Interrupt: "interrupt",
  Chat: "chat",
} as const;

export interface TurnTiming {
  prompt_tokens: number;
  completion_tokens: number;
  prefill_ms: number;
  decode_ms: number;
  total_ms: number;
  pp_rate: number;
  tg_rate: number;
}

export interface StreamEvent {
  type: string;
  text?: string;
  thinking?: string;
  tool_name?: string;
  tool_args?: string;
  output?: string;
  duration_ms?: number;
  error?: string;
  options?: string[];
  model?: string;
  context_tokens?: number;
  context_max?: number;
  timing?: TurnTiming;
  prompt_processed?: number;
  prompt_total?: number;
  source?: string;
  seq?: number;
  // SSE.Synced control frame fields.
  running?: boolean;
  committed_through?: number;
  // SSE.Notification payload (server-pushed).
  notification?: Notification;
  // SSE.Chat payload (project chatroom).
  chat?: ChatMessage;
}

export interface Notification {
  id: number;
  kind?: string;
  title: string;
  description?: string;
  prompt?: string;
  auto_run: boolean;
  status: string;
  session_id?: number;
  interface?: string;
}
export const NOTIFY_SESSION_TITLE = "session-title";

export interface Hat {
  name: string;
  description: string;
  system_prompt?: string;
  skills?: string[]; // visibility list (empty = all)
  overlay_skills?: string[]; // skills the hat carries its own variants of
  has_custom_prompt?: boolean; // system_prompt.md body is non-empty
  prompt_disabled?: boolean; // body kept but inactive (use_prompt: false)
}

export interface SkillInfo {
  name: string;
  description: string;
  source: string; // project | shared | user | hat
  also_in?: string[]; // scopes holding a shadowed copy (edits there are invisible)
}
export interface SkillFiles {
  name: string;
  scope?: string;
  files: Record<string, string>;
}
export interface SkillFile {
  name: string;
  path: string;
  scope: string;
  content: string;
}

export interface MemorySlot {
  key: string;
  value?: string;
}
export interface Memory {
  id: string; // "u.N" | "s.N"
  scope: string; // user | shared
  user_id?: number;
  content: string;
  slots?: MemorySlot[];
  source?: string;
  pinned: boolean;
  expires_at?: string;
  created_at: string;
}
export interface MemoryConflict {
  id: string;
  memory_a: string;
  memory_b: string;
  content_a: string;
  content_b: string;
  relationship: string;
  reason: string;
  confidence: number;
  detected_at: string;
  resolved_at?: string;
}
export interface SearchResult {
  id: string;
  content: string;
  slot_keys?: string[];
  score: number;
  scope?: string;
  source?: string;
}
export interface CreateMemoryRequest {
  scope: string;
  content: string;
  source?: string;
  expires_at?: string;
  project_id?: number; // required when scope is "project"
}

export interface MCPTool {
  name: string;
  description?: string;
}
export interface MCPServer {
  scope: string; // shared | user
  name: string;
  url: string;
  transport: string;
  auth_type: string; // none | header | oauth
  header_names?: string[];
  has_credential: boolean;
  enabled: boolean;
  auth_satisfied: boolean;
  needs_auth: boolean;
  tool_count?: number;
  error?: string;
  tools?: MCPTool[];
}
export interface MCPHeader {
  name: string;
  value: string;
}
export interface RegisterMCPRequest {
  scope: string;
  name: string;
  url: string;
  auth_type: string;
  headers?: MCPHeader[];
  oauth_scopes?: string[];
  enabled?: boolean;
}
export interface MCPTestResult {
  ok: boolean;
  tools?: string[];
  error?: string;
}
export interface MCPAuthStartResult {
  authorize_url: string;
}

export const CRON_KIND_JS = "js";
export const CRON_KIND_SKILL = "skill";
export interface CronJob {
  id: number;
  name: string;
  spec: string;
  kind: string; // js | skill
  target: string;
  prompt?: string;
  input?: string;
  enabled: boolean;
  notify: boolean;
  notify_channel?: string;
  next_run_at?: string;
  last_run_at?: string;
  last_status?: string;
  last_output?: string;
  created_at: string;
}
export interface CreateCronJobRequest {
  name: string;
  spec: string;
  kind: string;
  target: string;
  prompt?: string;
  input?: string;
  notify?: boolean;
  enabled?: boolean;
  notify_channel?: string; // "inapp" | "email" | "telegram"
}
export interface UpdateCronJobRequest {
  name?: string;
  spec?: string;
  kind?: string;
  target?: string;
  prompt?: string;
  input?: string;
  notify?: boolean;
  enabled?: boolean;
  notify_channel?: string; // "inapp" | "email" | "telegram"
}

export interface Document {
  id: number;
  title: string;
  uri: string;
  mime: string;
  created_by: number;
  created_at: string;
  scope?: string; // personal | shared | project
  chunks?: number;
  description?: string; // LLM-generated catalogue line
}
export interface DocChunkInfo {
  id: string;
  scope: string; // personal | shared | project
  document_id: number;
  title: string;
  mime: string;
  page?: number; // 1-based; absent when the source has no pages
  has_file: boolean;
  project_id?: number; // set for project-scoped chunks (which project holds it)
}
// Document alignment (the /documents/align side-by-side comparison).
export interface AlignSection {
  chunk_id: number;
  ord: number;
  page?: number;
  text: string;
}
export interface AlignPair {
  kind: string; // changed | matched | only_a | only_b
  similarity?: number;
  a_heading?: string;
  b_heading?: string;
  a: AlignSection[];
  b: AlignSection[];
}
export interface AlignDocMeta {
  ref: string; // scoped id, e.g. "u.2"
  title: string;
  scope: string;
  sections: number;
}
export interface AlignResult {
  mode: string; // versions | topical
  min_similarity: number;
  identical: number;
  a: AlignDocMeta;
  b: AlignDocMeta;
  pairs: AlignPair[];
}
export interface CreateDocumentRequest {
  title: string;
  uri: string;
  mime: string;
  content: string;
  scope?: string; // shared (default) | personal | project (needs project_id)
  project_id?: number;
}

export interface UsageRecord {
  id: number;
  user_id: number;
  session_id?: number;
  provider: string;
  model: string;
  prompt_tokens: number;
  completion_tokens: number;
  est_cost_usd: number;
  created_at: string;
}

export interface ContextCategory {
  name: string;
  tokens: number;
}

export interface ContextBreakdown {
  model: string;
  context_max: number;
  total: number;
  categories: ContextCategory[];
}
