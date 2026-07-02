import { writable } from "svelte/store";
import type { User, Project } from "./types";
import { setActiveProject } from "./api";

// The signed-in user (null = logged out).
export const user = writable<User | null>(null);

// The active project (null = working on personal sessions). When set, the session
// list shows the project's sessions and the chatroom side-pane appears.
export const activeProject = writable<Project | null>(null);
// Mirror the active project into the API client so project-scope-aware
// endpoints (skills) carry ?project=.
activeProject.subscribe((p) => setActiveProject(p?.id ?? 0));

// Whether the /project management sheet is open.
export const projectSheet = writable<boolean>(false);

// Which top-level view is shown (chat + management panels).
export type View =
  | "chat" | "skills" | "hats" | "memory" | "documents"
  | "mcp" | "cron" | "config" | "usage";
export const view = writable<View>("chat");

// The active session shown in the header (title is auto-updated by the
// server's auto-titler via a session-title notification).
export const session = writable<{ id: number; title: string }>({ id: 0, title: "" });

// Transient toast notifications.
export type Toast = { id: number; text: string; kind: "info" | "error" };
export const toasts = writable<Toast[]>([]);
let tid = 0;
export function toast(text: string, kind: "info" | "error" = "info"): void {
  const id = ++tid;
  toasts.update((t) => [...t, { id, text, kind }]);
  setTimeout(() => toasts.update((t) => t.filter((x) => x.id !== id)), 6000);
}
