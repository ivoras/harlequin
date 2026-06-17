// App-scoped controller for the project chatroom side-pane: one live socket to
// the active project's room, holding the message list shown in the pane.
import { ProjectChatSocket } from "./ws";
import { SSE } from "./types";
import type { ChatMessage, StreamEvent } from "./types";

class ProjectChat {
  messages = $state<ChatMessage[]>([]);
  connected = $state(false);

  private socket: ProjectChatSocket | null = null;
  private projectID = 0;

  // open connects to a project's chatroom (no-op if already on it).
  open(projectID: number): void {
    if (projectID === this.projectID && this.socket) return;
    this.close();
    this.projectID = projectID;
    this.messages = [];
    this.socket = new ProjectChatSocket(
      projectID,
      (ev) => this.onEvent(ev),
      (s) => (this.connected = s === "open"),
    );
    this.socket.open();
  }

  close(): void {
    this.socket?.close();
    this.socket = null;
    this.projectID = 0;
    this.messages = [];
    this.connected = false;
  }

  send(text: string): void {
    const t = text.trim();
    if (t) this.socket?.post(t);
  }

  private onEvent(ev: StreamEvent): void {
    if (ev.type !== SSE.Chat || !ev.chat) return;
    // Dedupe by id (history + live can overlap on reconnect).
    if (this.messages.some((m) => m.id === ev.chat!.id)) return;
    this.messages.push(ev.chat);
  }
}

export const pc = new ProjectChat();
