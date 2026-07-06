// SessionController owns the live session WebSocket and all chat + alert state at
// app scope, so it survives view switches (no socket churn) and the alert box can
// live in the app shell. Components (App, Chat) are thin views over this singleton.
import { api } from "./api";
import { SessionSocket } from "./ws";
import { SSE, NOTIFY_SESSION_TITLE } from "./types";
import type { Message, StreamEvent, Notification } from "./types";
import { session, toast } from "./stores";

export type Item =
  | { kind: "msg"; role: "user" | "assistant"; content: string }
  | { kind: "thinking"; text: string }
  | { kind: "tool"; name: string; args: string; output: string; ms: number; done: boolean }
  | { kind: "ask"; question: string; options: string[] }
  | { kind: "stats"; text: string }; // turn-end model/PP/TG/ctx summary

// fmtk renders a token count compactly (11k / 100k).
function fmtk(n: number): string {
  return n >= 1000 ? `${Math.round(n / 1000)}k` : String(n);
}

// turnStats builds the turn-end summary line, mirroring the TUI's formatTiming:
// PP/TG rates + wall clock, then model (basename) and context usage.
function turnStats(ev: StreamEvent): string {
  const parts: string[] = [];
  const t = ev.timing;
  if (t) {
    const secs = (ms: number) => (ms / 1000).toFixed(2);
    const pp = t.pp_rate > 0 ? `${Math.round(t.pp_rate)} tok/s (${fmtk(t.prompt_tokens)} tok / ${secs(t.prefill_ms)}s)` : "—";
    const tg = t.tg_rate > 0 ? `${t.tg_rate.toFixed(1)} tok/s (${fmtk(t.completion_tokens)} tok / ${secs(t.decode_ms)}s)` : "—";
    parts.push(`⏱ PP ${pp} · TG ${tg} · ${secs(t.total_ms)}s total`);
  }
  if (ev.model) {
    const model = ev.model.split("/").pop() || ev.model;
    parts.push(ev.context_max ? `${model} · ${fmtk(ev.context_tokens || 0)}/${fmtk(ev.context_max)} ctx` : model);
  }
  return parts.join(" · ");
}

class SessionController {
  // Chat state (reset per session).
  items = $state<Item[]>([]);
  loading = $state(false);
  ppLabel = $state("");
  queue = $state<string[]>([]);
  reconnecting = $state(false);
  // User-scoped alerts (persist across session switches, kept until dismissed).
  alerts = $state<Notification[]>([]);

  private socket: SessionSocket | null = null;
  private currentId = 0;
  // attachGen guards against overlapping attach() calls (even for the SAME
  // session id): while the history fetch is awaited there is no socket yet, so
  // a second attach could otherwise slip past the idempotence check and open a
  // second WebSocket — after which every event (and message) arrives twice.
  private attachGen = 0;
  private optimisticUser = 0; // user-message echoes to skip (rendered locally)
  private coldHistory: Message[] | null = null; // committed history awaiting the synced cut
  private streamingAssistant: Item | null = null;

  private projectID = 0; // non-zero when the active session belongs to a project

  // currentProjectID exposes the project the ATTACHED session actually belongs
  // to — authoritative for resolving refs found in that session's transcript,
  // unlike the global activeProject store, which tracks project-switcher UI
  // state and can drift out of sync with an already-open transcript (e.g. the
  // user switches projects while an older session's messages are still on
  // screen, or reattaches to a session via a stale/bookmarked URL).
  get currentProjectID(): number {
    return this.projectID;
  }

  // attach connects to session id (no-op if already attached). projectID > 0
  // attaches to a shared project session (history + WS under the project). Loads
  // committed history, then opens the socket so any in-flight turn replays and
  // continues live. Alerts are not reset — they are user-scoped.
  async attach(id: number, projectID = 0): Promise<void> {
    if (id === this.currentId && projectID === this.projectID && this.socket) return;
    const gen = ++this.attachGen;
    this.currentId = id;
    this.projectID = projectID;
    this.items = [];
    this.loading = false;
    this.optimisticUser = 0;
    this.streamingAssistant = null;
    this.queue = [];
    this.socket?.close();
    this.socket = null;
    try {
      this.coldHistory = projectID > 0 ? await api.projectMessages(projectID, id) : await api.getMessages(id);
    } catch (e) {
      this.coldHistory = [];
      toast((e as Error).message, "error");
    }
    if (gen !== this.attachGen) return; // a newer attach superseded this one
    this.socket = new SessionSocket(id, (ev) => this.onEvent(ev), (s) => (this.reconnecting = s === "reconnecting"), projectID);
    this.socket.open();
  }

  // detach tears down the connection (e.g. on logout).
  detach(): void {
    this.attachGen++; // invalidate any attach still awaiting its history fetch
    this.socket?.close();
    this.socket = null;
    this.currentId = 0;
    this.projectID = 0;
    this.items = [];
    this.alerts = [];
    this.queue = [];
    this.loading = false;
  }

  // clear wipes the current session's messages server-side and resets the
  // transcript — the next turn starts with a fresh context. Works for project
  // sessions too (the projectID routes the clear to the project database).
  async clear(): Promise<void> {
    if (!this.currentId) return;
    if (this.loading) {
      toast("a turn is in flight — stop it before /clear", "error");
      return;
    }
    try {
      await api.clearSession(this.currentId, this.projectID);
      this.items = [];
        this.streamingAssistant = null;
      toast("context cleared");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  // send is the composer entry point: queue while a turn is in flight, else start.
  send(text: string): void {
    const t = text.trim();
    if (!t || !this.currentId || !this.socket) return;
    if (this.loading) {
      this.queue.push(t);
      return;
    }
    this.sendText(t);
  }

  removeQueued(i: number): void {
    this.queue.splice(i, 1);
  }

  stop(): void {
    this.socket?.interrupt();
    this.loading = false;
  }

  dismissAlert(a: Notification): void {
    this.alerts = this.alerts.filter((x) => x.id !== a.id);
    api.ackNotification(a.id);
  }

  runAlert(a: Notification): void {
    this.dismissAlert(a);
    if (a.prompt && this.socket && !this.loading) this.sendText(a.prompt);
  }

  // exportTranscript downloads the current session transcript as Markdown.
  exportTranscript(): void {
    const parts: string[] = [];
    for (const it of this.items) {
      if (it.kind === "msg") parts.push(`## ${it.role === "user" ? "You" : "Assistant"}\n\n${it.content}`);
      else if (it.kind === "tool") parts.push(`> **tool ${it.name}** — ${it.output}`);
      else if (it.kind === "thinking") parts.push(`_thinking:_ ${it.text}`);
      else if (it.kind === "ask") parts.push(`## Assistant asked\n\n${it.question}`);
    }
    const blob = new Blob([parts.join("\n\n") + "\n"], { type: "text/markdown" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `harlequin-session-${this.currentId || "x"}.md`;
    a.click();
    URL.revokeObjectURL(url);
  }

  private sendText(text: string): void {
    this.items.push({ kind: "msg", role: "user", content: text });
    this.optimisticUser++; // skip the server's echo of this prompt
    this.loading = true;
    this.streamingAssistant = null;
    this.socket?.submit(text);
  }

  private getAssistant(): Item {
    if (!this.streamingAssistant) {
      this.items.push({ kind: "msg", role: "assistant", content: "" });
      this.streamingAssistant = this.items[this.items.length - 1];
    }
    return this.streamingAssistant;
  }

  private onEvent(ev: StreamEvent): void {
    switch (ev.type) {
      case SSE.Synced: {
        // Cold resume: render committed history (trimmed to before the in-flight
        // turn when running); the replayed buffer reconstructs that turn.
        if (this.coldHistory) {
          const through = ev.committed_through || 0;
          const msgs = ev.running ? this.coldHistory.filter((m) => m.id <= through) : this.coldHistory;
          this.items = msgs.flatMap(toItems);
          this.loading = !!ev.running;
          this.streamingAssistant = null;
          this.coldHistory = null;
        }
        break;
      }
      case SSE.UserMessage: {
        if (this.optimisticUser > 0) {
          this.optimisticUser--;
        } else {
          this.items.push({ kind: "msg", role: "user", content: ev.text || "" });
          this.loading = true;
          this.streamingAssistant = null;
        }
        break;
      }
      case SSE.PromptProgress: {
        const total = ev.prompt_total || 0;
        if (total > 0) {
          const pct = Math.floor(((ev.prompt_processed || 0) * 100) / total);
          const label = ev.source ? `${ev.source}: processing prompt` : "Processing prompt";
          this.ppLabel = `${label} ${pct}% (${ev.prompt_processed}/${total} tok)`;
        }
        break;
      }
      case SSE.Token: {
        this.ppLabel = "";
        const a = this.getAssistant();
        if (a.kind === "msg") a.content += ev.text || "";
        break;
      }
      case SSE.Thinking: {
        this.ppLabel = "";
        let last = this.items.at(-1);
        if (!last || last.kind !== "thinking") {
          this.items.push({ kind: "thinking", text: "" });
          last = this.items.at(-1);
        }
        if (last && last.kind === "thinking") last.text += ev.thinking || "";
        break;
      }
      case SSE.ToolCall:
        this.streamingAssistant = null; // a fresh assistant bubble follows the tool
        this.items.push({ kind: "tool", name: ev.tool_name || "tool", args: ev.tool_args || "", output: "", ms: 0, done: false });
        break;
      case SSE.ToolResult:
        this.ppLabel = "";
        for (let i = this.items.length - 1; i >= 0; i--) {
          const it = this.items[i];
          if (it.kind === "tool" && !it.done) {
            it.output = ev.output || "";
            it.ms = ev.duration_ms || 0;
            it.done = true;
            break;
          }
        }
        break;
      case SSE.AskUser:
        this.items.push({ kind: "ask", question: ev.text || "", options: ev.options || [] });
        break;
      case SSE.Notification: {
        const n = ev.notification;
        if (!n) break;
        if (n.kind === NOTIFY_SESSION_TITLE) {
          if (n.session_id === this.currentId) session.update((s) => ({ ...s, title: n.title }));
          api.ackNotification(n.id);
        } else if (!n.auto_run) {
          // Passive notification: persistent alert box (deduped), kept until dismissed.
          if (!this.alerts.some((a) => a.id === n.id)) this.alerts.push(n);
        }
        // auto_run notifications are left for a client that runs them.
        break;
      }
      case SSE.Error:
        toast(ev.error || "error", "error");
        break;
      case SSE.Done: {
        // Turn-end stats live inline in the transcript (like the TUI), not in
        // a separate element below the composer.
        const stats = turnStats(ev);
        if (stats) this.items.push({ kind: "stats", text: stats });
        this.loading = false;
        this.ppLabel = "";
        this.streamingAssistant = null;
        if (this.queue.length > 0) {
          const next = this.queue.shift()!;
          this.sendText(next);
        }
        break;
      }
    }
  }
}

function toItems(m: Message): Item[] {
  if (m.role === "user" || m.role === "assistant") {
    return m.content ? [{ kind: "msg", role: m.role, content: m.content }] : [];
  }
  if (m.role === "tool") {
    return [{ kind: "tool", name: m.name || "tool", args: "", output: m.content, ms: 0, done: true }];
  }
  return [];
}

// The app-wide singleton.
export const sc = new SessionController();
