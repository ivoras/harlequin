// Shared project-workspace switching, usable from any view (the /project
// sheet, the Projects tab, …). Switching opens one of the project's sessions
// (or assigns a fresh one) and returns to the chat view.
import { api } from "./api";
import { activeProject, session, wornHat, view, toast, user } from "./stores";
import { get } from "svelte/store";
import { isElevated, type Project, type ProjectMember } from "./types";

// projectBylines maps project id -> "by <creator email>" for projects whose
// name is not unique in the given list (empty string when the name is unique
// or no email is known). Callers append it as a muted suffix so duplicate
// names stay distinguishable.
export function projectBylines(projects: Project[]): Map<number, string> {
  const counts = new Map<string, number>();
  for (const p of projects) {
    const k = p.name.trim().toLowerCase();
    counts.set(k, (counts.get(k) ?? 0) + 1);
  }
  const out = new Map<number, string>();
  for (const p of projects) {
    const dup = (counts.get(p.name.trim().toLowerCase()) ?? 0) > 1;
    out.set(p.id, dup && p.created_by_email ? `by ${p.created_by_email}` : "");
  }
  return out;
}

// cleanTitle blanks the generic placeholder titles for the header.
export function cleanTitle(t: string): string {
  return t === "Session" || t === "New session" || t === "New conversation" ? "" : t;
}

// switchToProject makes the project the active chat workspace. The target
// session is resolved BEFORE the stores change, and the project + session are
// set together: the app attaches whenever either changes, so flipping the
// project first would briefly attach the old personal session under the
// project (a 404 on its history).
export async function switchToProject(p: Project): Promise<void> {
  try {
    const ps = await api.listProjectSessions(p.id);
    let next: { id: number; title: string; hat: string };
    if (ps.length > 0) {
      next = { id: ps[0].id, title: cleanTitle(ps[0].title), hat: ps[0].hat ?? "" };
    } else {
      const c = await api.createSession("Session", "");
      const { session_id } = await api.assignSession(p.id, c.id);
      next = { id: session_id, title: "", hat: "" };
    }
    activeProject.set(p);
    session.set({ id: next.id, title: next.title });
    wornHat.set(next.hat);
    view.set("chat");
  } catch (e) {
    toast((e as Error).message, "error");
  }
}

// leaveActiveProject returns to a fresh personal session (created first, for
// the same reason as above).
export async function leaveActiveProject(): Promise<void> {
  try {
    const c = await api.createSession("Session", "");
    activeProject.set(null);
    session.set({ id: c.id, title: cleanTitle(c.title) });
    wornHat.set("");
    view.set("chat");
  } catch (e) {
    toast((e as Error).message, "error");
  }
}

// canDeleteProject mirrors the server rule: the creator or an owner/admin.
export function canDeleteProject(p: Project): boolean {
  const u = get(user);
  return !!u && (p.created_by === u.id || isElevated(u.role));
}

// canRemoveMember: the project's creator and owners/admins may remove other
// members — except the creator (delete the project instead) and themselves
// (that's "Depart"). Mirrors the server's check.
export function canRemoveMember(p: Project, memberID: number): boolean {
  const u = get(user);
  return canDeleteProject(p) && memberID !== p.created_by && memberID !== u?.id;
}

// removeMemberFlow confirms and removes a member; resolves true when removed.
export async function removeMemberFlow(p: Project, m: ProjectMember): Promise<boolean> {
  if (!confirm(`Remove ${m.email} from "${p.name}"?\n\nThey lose access to its sessions, documents, memories and team chat right away. You can invite them again later.`)) return false;
  try {
    await api.removeProjectMember(p.id, m.user_id);
    toast(`removed ${m.email} from ${p.name}`);
    return true;
  } catch (e) {
    toast((e as Error).message, "error");
    return false;
  }
}

// deleteProjectFlow asks the user to type the project name, then permanently
// deletes the project. Resolves true when it was deleted (the caller refreshes
// its lists); leaves the active workspace first if it was the deleted project.
export async function deleteProjectFlow(p: Project): Promise<boolean> {
  const typed = prompt(
    `This permanently deletes "${p.name}" for ALL members: its sessions, documents, memories, chat and files.\n\nType the project name to confirm:`,
  );
  if (typed === null) return false;
  if (typed !== p.name) {
    toast("name did not match — nothing deleted", "error");
    return false;
  }
  try {
    const wasActive = get(activeProject)?.id === p.id;
    await api.deleteProject(p.id, p.name);
    toast("deleted " + p.name);
    if (wasActive) await leaveActiveProject();
    return true;
  } catch (e) {
    toast((e as Error).message, "error");
    return false;
  }
}
