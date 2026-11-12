// Boot-time session/project restore shared by both web UIs: the active session
// and project live in the URL (?s=&p=) so a refresh (or a link opened in the
// other UI) resumes the same session in the same project.
import { get } from "svelte/store";
import { api } from "./api";
import { session, wornHat, activeProject, toast } from "./stores";
import { switchToProject, cleanTitle } from "./project";

export function intParam(name: string): number {
  const v = new URLSearchParams(location.search).get(name);
  const n = v ? parseInt(v, 10) : 0;
  return Number.isFinite(n) && n > 0 ? n : 0;
}

export function setURLParams(sessionID: number, projectID: number): void {
  const url = new URL(location.href);
  if (sessionID) url.searchParams.set("s", String(sessionID));
  else url.searchParams.delete("s");
  if (projectID) url.searchParams.set("p", String(projectID));
  else url.searchParams.delete("p");
  history.replaceState(null, "", url);
}

let creating = false;

// ensureSession starts a fresh session in the current workspace and makes it
// current: inside a project it is created and assigned to that project (the
// session is attached under the project, so a plain personal one would 404).
export async function ensureSession(): Promise<void> {
  if (creating) return;
  creating = true;
  try {
    const c = await api.createSession("New session", "");
    // In a project the session must live in the project db (assigning moves it
    // and returns the new id); otherwise the project route 404s on the personal id.
    const proj = get(activeProject);
    if (proj) {
      const { session_id } = await api.assignSession(proj.id, c.id);
      session.set({ id: session_id, title: "" });
    } else {
      session.set({ id: c.id, title: cleanTitle(c.title) });
    }
    wornHat.set("");
  } catch (e) {
    toast((e as Error).message, "error");
  } finally {
    creating = false;
  }
}

// initSession resumes the project (?p=) and session (?s=) named in the URL, if
// present; otherwise it starts a fresh personal session. A project session lives
// in that project's own session list, not the personal one, so the project
// must be restored FIRST — otherwise the session lookup misses it entirely
// and the app silently falls back to an unrelated (or brand new) session.
export async function initSession(): Promise<void> {
  const pid = intParam("p");
  const id = intParam("s");
  if (pid) {
    try {
      const p = await api.getProject(pid);
      activeProject.set(p);
      if (id) {
        const list = await api.listProjectSessions(pid);
        const found = list.find((c) => c.id === id);
        if (found) {
          session.set({ id, title: cleanTitle(found.title) });
          wornHat.set(found.hat ?? "");
          return;
        }
        // The session named in the URL no longer belongs to this project
        // (deleted, reassigned) — fall through to opening a project session.
      }
      await switchToProject(p);
      return;
    } catch {
      // Project gone or no longer a member: drop it and fall through to the
      // plain personal-session path below rather than getting stuck.
      activeProject.set(null);
    }
  }
  if (id) {
    try {
      const list = await api.listSessions();
      const found = list.find((c) => c.id === id);
      session.set({ id, title: found ? cleanTitle(found.title) : "" });
      wornHat.set(found?.hat ?? "");
      return;
    } catch {
      /* fall through to a new session */
    }
  }
  await ensureSession();
}

// authorizeMCP sends the browser (same tab: popups opened after an await are
// blocked, especially on mobile) to an OAuth MCP server's consent page; the
// server's callback redirects back here, see consumeMCPOAuthResult.
// shortcut: return path is relative to the API origin, so a web UI served from a
// different origin than the API (custom base URL) lands on the API host instead.
export async function authorizeMCP(scope: string, name: string): Promise<void> {
  try {
    const r = await api.startMCPOAuth(scope, name, location.pathname + location.search);
    location.href = r.authorize_url;
  } catch (e) {
    toast((e as Error).message, "error");
  }
}

// consumeMCPOAuthResult reports (and strips from the URL) the outcome the MCP
// OAuth callback appended when redirecting back. True if there was one.
export function consumeMCPOAuthResult(): boolean {
  const url = new URL(location.href);
  const res = url.searchParams.get("mcp_oauth");
  if (!res) return false;
  toast(url.searchParams.get("mcp_msg") || (res === "ok" ? "Authorized" : "Authorization failed"), res === "ok" ? "info" : "error");
  url.searchParams.delete("mcp_oauth");
  url.searchParams.delete("mcp_msg");
  history.replaceState(null, "", url);
  return true;
}

// mcpSignInTarget extracts the server from an "mcp-signin:<scope>/<name>"
// reminder, or null for any other notification. Names can't contain "/".
export function mcpSignInTarget(kind?: string): { scope: string; name: string } | null {
  const pre = "mcp-signin:";
  if (!kind?.startsWith(pre)) return null;
  const rest = kind.slice(pre.length);
  const i = rest.indexOf("/");
  return i > 0 ? { scope: rest.slice(0, i), name: rest.slice(i + 1) } : null;
}
