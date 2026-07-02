// Typed REST client mirroring internal/client/apiclient. Same-origin by default
// (base ""), so the server (or nginx) serves both the SPA and /api/v1. A base URL
// can be overridden for a standalone/decoupled deployment.
import { INTERFACE } from "./types";
import type {
  LoginResponse, User, Session, Message, Hat, SkillInfo, SkillFiles, SkillFile,
  Memory, MemoryConflict, SearchResult, MCPServer, RegisterMCPRequest,
  MCPTestResult, MCPAuthStartResult, CronJob, CreateCronJobRequest,
  UpdateCronJobRequest, UsageRecord, Notification, Document, CreateDocumentRequest,
  CreateMemoryRequest, Project, ProjectInvite,
} from "./types";

const TOKEN_KEY = "harlequin.token";
const BASE_KEY = "harlequin.apiBase";

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || "";
}
export function setToken(t: string): void {
  if (t) localStorage.setItem(TOKEN_KEY, t);
  else localStorage.removeItem(TOKEN_KEY);
}
export function getBase(): string {
  return localStorage.getItem(BASE_KEY) || "";
}
export function setBase(b: string): void {
  const v = b.replace(/\/+$/, "");
  if (v) localStorage.setItem(BASE_KEY, v);
  else localStorage.removeItem(BASE_KEY);
}

export function apiUrl(path: string): string {
  return getBase() + "/api/v1" + path;
}
export function authHeaders(extra?: Record<string, string>): Record<string, string> {
  const h: Record<string, string> = { "X-Harlequin-Interface": INTERFACE, ...(extra || {}) };
  const tok = getToken();
  if (tok) h["Authorization"] = "Bearer " + tok;
  return h;
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers = authHeaders();
  let bodyStr: string | undefined;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    bodyStr = JSON.stringify(body);
  }
  const res = await fetch(apiUrl(path), { method, headers, body: bodyStr });
  if (!res.ok) {
    let msg = res.status + " " + res.statusText;
    try {
      const e = await res.json();
      if (e && e.error) msg = e.error;
    } catch { /* keep status */ }
    if (res.status === 401) setToken("");
    throw new Error(msg);
  }
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

// Go serializes an empty slice as JSON `null`, so list endpoints can return null
// for an empty result. Coalesce to [] so callers can always iterate.
async function reqList<T>(method: string, path: string, body?: unknown): Promise<T[]> {
  return (await req<T[] | null>(method, path, body)) ?? [];
}

const q = (s: string) => encodeURIComponent(s);
// Escape each segment of a file relpath, keeping the slashes routable.
const qpath = (p: string) => p.split("/").map(q).join("/");
const mcpRef = (scope: string, name: string) => `?scope=${q(scope)}&name=${q(name)}`;

// The active project id (0 = none), appended as ?project= to project-scope-aware
// endpoints (skills) so the server resolves the project scope. Kept in sync with
// the activeProject store (see stores.ts).
let activeProjectId = 0;
export function setActiveProject(id: number): void {
  activeProjectId = id;
}
const withProject = (path: string) =>
  activeProjectId ? `${path}${path.includes("?") ? "&" : "?"}project=${activeProjectId}` : path;

// uploadDoc posts a file as multipart/form-data (the server extracts text — PDFs
// via PDFium — and ingests it). We must NOT set Content-Type: the browser adds
// the multipart boundary itself.
async function uploadDoc(file: File, title?: string, scope?: string, projectID?: number): Promise<Document> {
  const fd = new FormData();
  fd.append("file", file);
  if (title) fd.append("title", title);
  if (scope) fd.append("scope", scope);
  if (projectID) fd.append("project_id", String(projectID));
  const res = await fetch(apiUrl("/documents"), { method: "POST", headers: authHeaders(), body: fd });
  if (!res.ok) {
    let msg = res.status + " " + res.statusText;
    try { const e = await res.json(); if (e && e.error) msg = e.error; } catch { /* keep status */ }
    if (res.status === 401) setToken("");
    throw new Error(msg);
  }
  return (await res.json()) as Document;
}

export const api = {
  // auth / user
  login: (email: string, password: string) =>
    req<LoginResponse>("POST", "/auth/login", { email, password }),
  registrationEnabled: () =>
    req<{ enabled: boolean }>("GET", "/auth/registration"),
  register: (email: string, password: string) =>
    req<{ status: string; email: string }>("POST", "/auth/register", { email, password }),
  verify: (email: string, code: string) =>
    req<LoginResponse>("POST", "/auth/verify", { email, code }),
  logout: () => req<void>("POST", "/auth/logout"),
  me: () => req<User>("GET", "/me"),

  // sessions
  listSessions: (query = "") =>
    reqList<Session>("GET", "/sessions" + (query ? `?q=${q(query)}` : "")),
  createSession: (title: string, hat = "") =>
    req<Session>("POST", "/sessions", { title, hat }),
  getMessages: (id: number) => reqList<Message>("GET", `/sessions/${id}/messages`),
  deleteSession: (id: number) => req<void>("DELETE", `/sessions/${id}`),
  setSessionHat: (id: number, hat: string) =>
    req<void>("POST", `/sessions/${id}/hat`, { hat }),

  // hats
  listHats: () => reqList<Hat>("GET", "/hats"),
  getHat: (name: string) => req<Hat>("GET", `/hats/${q(name)}`),
  createHat: (name: string, description: string) => req<void>("POST", "/hats", { name, description }),
  deleteHat: (name: string) => req<void>("DELETE", `/hats/${q(name)}`),
  getHatFiles: (name: string) => req<{ name: string; files: Record<string, string> }>("GET", `/hats/${q(name)}/files`),
  getHatFile: (name: string, path: string) =>
    req<{ content: string }>("GET", `/hats/${q(name)}/files/${qpath(path)}`),
  putHatFile: (name: string, path: string, content: string) =>
    req<void>("PUT", `/hats/${q(name)}/files/${qpath(path)}`, { content }),
  addHatSkill: (hat: string, skill: string) => req<void>("POST", `/hats/${q(hat)}/skills`, { skill }),
  setHatPromptEnabled: (hat: string, enabled: boolean) =>
    req<void>("POST", `/hats/${q(hat)}/prompt`, { enabled }),
  getSystemPromptTemplate: () => req<{ content: string }>("GET", "/system-prompt"),
  removeHatSkill: (hat: string, skill: string) => req<void>("DELETE", `/hats/${q(hat)}/skills/${q(skill)}`),

  // skills
  listSkills: () => reqList<SkillInfo>("GET", withProject("/skills")),
  getSkill: (name: string) => req<SkillFiles>("GET", withProject(`/skills/${q(name)}`)),
  putSkill: (name: string, scope: string, files: Record<string, string>) =>
    req<void>("PUT", withProject(`/skills/${q(name)}`), { name, scope, files }),
  resetSkill: (name: string, scope = "") =>
    req<void>("DELETE", withProject(`/skills/${q(name)}${scope ? `?scope=${q(scope)}` : ""}`)),
  publishSkill: (name: string) => req<void>("POST", `/skills/${q(name)}/publish`),
  createSkill: (name: string, description: string, scope = "") =>
    req<void>("POST", withProject("/skills"), { name, description, scope }),
  getSkillFile: (name: string, path: string) =>
    req<SkillFile>("GET", withProject(`/skills/${q(name)}/files/${qpath(path)}`)),
  putSkillFile: (name: string, path: string, scope: string, content: string) =>
    req<void>("PUT", withProject(`/skills/${q(name)}/files/${qpath(path)}`), { scope, content }),

  // memory
  listMemory: (scope = "") => reqList<Memory>("GET", "/memory" + (scope ? `?scope=${q(scope)}` : "")),
  findMemory: (query: string) => reqList<Memory>("GET", `/memory/find?q=${q(query)}`),
  searchMemory: (query: string, scope = "") =>
    reqList<SearchResult>("GET", `/memory/search?q=${q(query)}${scope ? `&scope=${q(scope)}` : ""}`),
  getMemory: (id: string) => req<Memory>("GET", `/memory/${q(id)}`),
  createMemory: (r: CreateMemoryRequest) => req<Memory>("POST", "/memory", r),
  deleteMemory: (id: string) => req<void>("DELETE", `/memory/${q(id)}`),
  listMemoryConflicts: () => reqList<MemoryConflict>("GET", "/memory/conflicts"),
  resolveMemoryConflict: (id: string) => req<void>("POST", `/memory/conflicts/${q(id)}/resolve`),

  // documents
  listDocuments: (projectID = 0) =>
    reqList<Document>("GET", `/documents${projectID ? `?project=${projectID}` : ""}`),
  createDocument: (r: CreateDocumentRequest) => req<Document>("POST", "/documents", r),
  uploadDocument: (file: File, title?: string, scope?: string, projectID?: number) =>
    uploadDoc(file, title, scope, projectID),
  deleteDocument: (id: number, scope = "", projectID = 0) => {
    const v = new URLSearchParams();
    if (scope) v.set("scope", scope);
    if (projectID) v.set("project", String(projectID));
    const qs = v.toString();
    return req<void>("DELETE", `/documents/${id}${qs ? `?${qs}` : ""}`);
  },
  searchDocuments: (query: string) => reqList<SearchResult>("GET", `/documents/search?q=${q(query)}`),

  // mcp
  listMCP: () => reqList<MCPServer>("GET", "/mcp"),
  getMCP: (scope: string, name: string) => req<MCPServer>("GET", "/mcp/server" + mcpRef(scope, name)),
  registerMCP: (r: RegisterMCPRequest) => req<void>("POST", "/mcp", r),
  updateMCP: (scope: string, name: string, r: RegisterMCPRequest) =>
    req<void>("PATCH", "/mcp/server" + mcpRef(scope, name), r),
  deleteMCP: (scope: string, name: string) => req<void>("DELETE", "/mcp/server" + mcpRef(scope, name)),
  testMCP: (scope: string, name: string) =>
    req<MCPTestResult>("POST", "/mcp/server/test" + mcpRef(scope, name)),
  startMCPOAuth: (scope: string, name: string) =>
    req<MCPAuthStartResult>("POST", "/mcp/server/oauth/start" + mcpRef(scope, name)),

  // cron
  listCron: () => reqList<CronJob>("GET", "/cron"),
  createCron: (r: CreateCronJobRequest) => req<CronJob>("POST", "/cron", r),
  getCron: (id: number) => req<CronJob>("GET", `/cron/${id}`),
  updateCron: (id: number, r: UpdateCronJobRequest) => req<CronJob>("PATCH", `/cron/${id}`, r),
  deleteCron: (id: number) => req<void>("DELETE", `/cron/${id}`),
  runCron: (id: number) => req<void>("POST", `/cron/${id}/run`),

  // config
  getConfig: async () => (await req<Record<string, string> | null>("GET", "/config")) ?? {},
  setConfig: (key: string, value: string) => req<void>("PUT", `/config/${q(key)}`, { value }),
  deleteConfig: (key: string) => req<void>("DELETE", `/config/${q(key)}`),

  // notifications
  listNotifications: () => reqList<Notification>("GET", "/notifications"),
  ackNotification: (id: number) => req<void>("POST", `/notifications/${id}/ack`),
  dismissNotification: (id: number) => req<void>("POST", `/notifications/${id}/dismiss`),
  broadcastAlert: (message: string) => req<void>("POST", "/alerts", { message }),

  // projects
  listProjects: () => reqList<Project>("GET", "/projects"),
  createProject: (name: string) => req<Project>("POST", "/projects", { name }),
  getProject: (id: number) => req<Project>("GET", `/projects/${id}`),
  inviteToProject: (id: number, email: string) => req<void>("POST", `/projects/${id}/invite`, { email }),
  departProject: (id: number) => req<void>("POST", `/projects/${id}/depart`),
  listProjectInvites: () => reqList<ProjectInvite>("GET", "/projects/invites"),
  acceptInvite: (inviteID: number) => req<{ project_id: number }>("POST", `/projects/invites/${inviteID}/accept`),
  listProjectSessions: (id: number) => reqList<Session>("GET", `/projects/${id}/sessions`),
  assignSession: (projectID: number, sessionID: number) =>
    req<{ session_id: number }>("POST", `/projects/${projectID}/sessions/${sessionID}`),
  projectMessages: (projectID: number, sessionID: number) =>
    reqList<Message>("GET", `/projects/${projectID}/messages?sid=${sessionID}`),

  // misc
  usage: () => reqList<UsageRecord>("GET", "/usage"),
};
