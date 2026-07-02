// Shared project-workspace switching, usable from any view (the /project
// sheet, the Projects tab, …). Switching opens one of the project's sessions
// (or assigns a fresh one) and returns to the chat view.
import { api } from "./api";
import { activeProject, session, wornHat, view, toast } from "./stores";
import type { Project } from "./types";

// cleanTitle blanks the generic placeholder titles for the header.
export function cleanTitle(t: string): string {
  return t === "Session" || t === "New session" || t === "New conversation" ? "" : t;
}

// switchToProject makes the project the active chat workspace.
export async function switchToProject(p: Project): Promise<void> {
  activeProject.set(p);
  try {
    const ps = await api.listProjectSessions(p.id);
    if (ps.length > 0) {
      session.set({ id: ps[0].id, title: cleanTitle(ps[0].title) });
      wornHat.set(ps[0].hat ?? "");
    } else {
      const c = await api.createSession("Session", "");
      const { session_id } = await api.assignSession(p.id, c.id);
      session.set({ id: session_id, title: "" });
      wornHat.set("");
    }
    view.set("chat");
  } catch (e) {
    toast((e as Error).message, "error");
  }
}

// leaveActiveProject returns to a fresh personal session.
export async function leaveActiveProject(): Promise<void> {
  activeProject.set(null);
  try {
    const c = await api.createSession("Session", "");
    session.set({ id: c.id, title: cleanTitle(c.title) });
    wornHat.set("");
    view.set("chat");
  } catch (e) {
    toast((e as Error).message, "error");
  }
}
