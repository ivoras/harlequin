// Unread team-chat messages across the user's projects. The server keeps the
// per-project read marker in the user's own database; this module polls the
// comparison and reports what the user has just seen.
import { api } from "../lib/api";
import type { ChatUnreadProject } from "../lib/types";

const POLL_MS = 45_000;

class Unread {
  projects = $state<ChatUnreadProject[]>([]);
  total = $derived(this.projects.reduce((n, p) => n + p.count, 0));
  // Whether this tab is in front: messages shown in a hidden tab are not seen.
  visible = $state(typeof document === "undefined" ? true : !document.hidden);

  private timer: ReturnType<typeof setInterval> | null = null;
  private sent = new Map<number, number>(); // project -> highest id marked this session

  async refresh(): Promise<void> {
    try {
      this.projects = await api.chatUnread();
    } catch {
      /* transient: keep the last figures */
    }
  }

  start(): void {
    if (this.timer) return;
    this.refresh();
    this.timer = setInterval(() => this.refresh(), POLL_MS);
    document.addEventListener("visibilitychange", this.onVis);
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
    document.removeEventListener("visibilitychange", this.onVis);
    this.projects = [];
    this.sent.clear();
  }

  private onVis = () => {
    this.visible = !document.hidden;
    if (this.visible) this.refresh();
  };

  // markRead records that the user saw the project's chat up to lastID, and
  // drops the project from the list at once.
  async markRead(projectID: number, lastID: number): Promise<void> {
    if (lastID <= (this.sent.get(projectID) ?? 0)) return;
    this.sent.set(projectID, lastID);
    this.projects = this.projects.filter((p) => p.project_id !== projectID); // anything newer reappears on the refresh below
    try {
      await api.markChatRead(projectID, lastID);
    } catch {
      this.sent.delete(projectID); // retry on the next change
    }
    this.refresh();
  }
}

export const unread = new Unread();
