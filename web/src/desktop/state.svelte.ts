// Desktop shell state: layout (sidebar / inspector / overlays), the workspace
// lists the sidebar and command palette share (projects, sessions, documents),
// and the upload queue fed by drag-and-drop and the upload buttons.
import { get } from "svelte/store";
import { api } from "../lib/api";
import { activeProject, session, wornHat, view, toast } from "../lib/stores";
import { cleanTitle } from "../lib/project";
import { uploadWithProgress, ingestLabel } from "../lib/ingest";
import type { Composer } from "../lib/composer.svelte";
import type { Document, Project, ProjectInvite, Session } from "../lib/types";

function load<T>(key: string, dflt: T): T {
  try {
    const v = localStorage.getItem(key);
    return v === null ? dflt : (JSON.parse(v) as T);
  } catch {
    return dflt;
  }
}
export function persist(key: string, v: unknown): void {
  try {
    localStorage.setItem(key, JSON.stringify(v));
  } catch {
    /* per-browser convenience only */
  }
}

export type InspectorTab = "team" | "document" | "session";
export type SettingsTab = "general" | "login" | "users" | "hats" | "mcp" | "config" | "usage" | "audit";

// Where an upload lands. scope mirrors CreateDocumentRequest.scope.
export interface UploadTarget {
  scope: "personal" | "shared" | "project";
  projectID: number;
  label: string; // shown in the drop overlay / tray ("Personal files", "Project Foo")
}

export interface Upload {
  id: number;
  name: string;
  target: string;
  label: string;
  state: "queued" | "running" | "done" | "error";
  error?: string;
}

let uploadSeq = 0;

class DesktopUI {
  sidebarOpen = $state(load("harlequin.d.sidebar", true));
  inspectorOpen = $state(load("harlequin.d.inspector", false));
  inspectorTab = $state<InspectorTab>(load("harlequin.d.inspectorTab", "session"));
  inspectorWidth = $state(load("harlequin.d.inspectorWidth", 400));
  paletteOpen = $state(false);
  settingsTab = $state<SettingsTab | null>(null);

  projects = $state<Project[]>([]);
  invites = $state<ProjectInvite[]>([]);
  // Sessions of the current workspace (the active project's, or personal).
  sessions = $state<Session[]>([]);
  // Every corpus visible in the current workspace (personal + shared, plus
  // the active project's), scope-tagged.
  docs = $state<Document[]>([]);
  // Bumped whenever documents change, so views holding their own lists reload.
  docsVersion = $state(0);

  uploads = $state<Upload[]>([]);
  // A view may claim drops for its own target (e.g. Files with a chosen
  // destination, Projects with a selected project); null = the workspace default.
  dropTarget = $state<UploadTarget | null>(null);
  // The chat view's composer while it is mounted: drops on the chat attach to
  // the message being written instead of just adding files.
  chatComposer: Composer | null = null;

  private uploadChain: Promise<void> = Promise.resolve();

  openInspector(tab: InspectorTab): void {
    this.inspectorTab = tab;
    this.inspectorOpen = true;
  }

  openSettings(tab: SettingsTab = "general"): void {
    this.settingsTab = tab;
  }

  async loadProjects(): Promise<void> {
    try {
      [this.projects, this.invites] = await Promise.all([api.listProjects(), api.listProjectInvites()]);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  async loadSessions(): Promise<void> {
    const p = get(activeProject);
    try {
      const list = p ? await api.listProjectSessions(p.id) : await api.listSessions();
      // A late response for a workspace we already left must not overwrite.
      if ((get(activeProject)?.id ?? 0) === (p?.id ?? 0)) this.sessions = list;
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  async loadDocs(): Promise<void> {
    const pid = get(activeProject)?.id ?? 0;
    try {
      const list = await api.listDocuments(pid);
      if ((get(activeProject)?.id ?? 0) === pid) this.docs = list;
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  docsChanged(): void {
    this.docsVersion++;
    this.loadDocs();
  }

  // defaultTarget is where files go when nothing more specific is chosen: the
  // active project (it is the shared workspace the user is in), else personal.
  defaultTarget(): UploadTarget {
    const p = get(activeProject);
    return p
      ? { scope: "project", projectID: p.id, label: `project “${p.name}”` }
      : { scope: "personal", projectID: 0, label: "your personal files" };
  }

  // upload queues files for ingestion into target, one at a time (each
  // ingest extracts + embeds server-side; running them serially keeps the
  // server responsive and the progress readable). Failures stay in the tray
  // until dismissed.
  upload(files: File[], target: UploadTarget = this.dropTarget ?? this.defaultTarget()): void {
    for (const file of files) {
      const id = ++uploadSeq;
      this.uploads.push({ id, name: file.name, target: target.label, label: "queued", state: "queued" });
      this.uploadChain = this.uploadChain.then(() => this.runUpload(id, file, target));
    }
  }

  private async runUpload(id: number, file: File, target: UploadTarget): Promise<void> {
    const set = (patch: Partial<Upload>) => {
      const u = this.uploads.find((x) => x.id === id);
      if (u) Object.assign(u, patch);
    };
    if (!this.uploads.some((x) => x.id === id)) return; // dismissed while queued
    set({ state: "running", label: "uploading…" });
    try {
      const d = await uploadWithProgress(
        file,
        { scope: target.scope, projectID: target.projectID || undefined },
        (p) => set({ label: ingestLabel(p) }),
      );
      set({ state: "done", label: `added as “${d.title}”` });
      this.docsChanged();
      // Finished rows clear themselves; errors stay until dismissed.
      setTimeout(() => this.dismissUpload(id), 5000);
    } catch (e) {
      set({ state: "error", label: "failed", error: (e as Error).message });
    }
  }

  // deleteDoc confirms, then permanently deletes a document (file, extracted
  // Markdown text and search entries). projectID is the active project, used
  // for project-scope documents. Always re-lists afterwards, even on error.
  async deleteDoc(d: Document, projectID = 0): Promise<void> {
    if (!confirm(`Delete "${d.title}"? This permanently removes the file, its extracted Markdown text and its search entries, for everyone who can see it. It cannot be undone.`)) return;
    try {
      await api.deleteDocument(d.id, d.scope ?? "", d.scope === "project" ? projectID : 0);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      this.docsChanged();
    }
  }

  dismissUpload(id: number): void {
    this.uploads = this.uploads.filter((u) => u.id !== id);
  }
}

export const ui = new DesktopUI();

// openSession makes c the current session and shows the chat.
export function openSession(c: Session): void {
  session.set({ id: c.id, title: cleanTitle(c.title) });
  wornHat.set(c.hat ?? "");
  view.set("chat");
}

// Platform-appropriate modifier label for shortcut hints.
export const MOD = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform) ? "⌘" : "Ctrl";

// docRef is a document's unambiguous reference id — the same form chat
// citations use ("u.N" personal, "s.N" shared, "p<project>.N" project).
export function docRef(d: Document, projectID: number): string {
  if (d.scope === "personal") return `u.${d.id}`;
  if (d.scope === "project") return `p${projectID}.${d.id}`;
  return `s.${d.id}`;
}

// relTime renders "time since" compactly ("now", "3m", "2h", "5d").
export function relTime(iso: string): string {
  const secs = (Date.now() - new Date(iso).getTime()) / 1000;
  if (!Number.isFinite(secs) || secs < 60) return "now";
  if (secs < 3600) return Math.floor(secs / 60) + "m";
  if (secs < 86400) return Math.floor(secs / 3600) + "h";
  return Math.floor(secs / 86400) + "d";
}

// mimeLabel shortens a MIME type for listings ("PDF", "DOCX", "Text", …).
export function mimeLabel(m: string): string {
  if (m === "application/pdf") return "PDF";
  if (m.includes("wordprocessingml")) return "DOCX";
  if (m === "text/markdown") return "Markdown";
  if (m.startsWith("text/")) return "Text";
  const tail = m.split("/").pop() || m;
  return tail.length > 12 ? tail.slice(0, 12) + "…" : tail;
}
