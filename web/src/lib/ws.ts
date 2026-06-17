// Live session over WebSocket. The server runs the turn independently of this
// connection, so a drop/refresh does not cancel it — the socket auto-reconnects
// and resumes from the last seen event seq. Streaming is WebSocket-only.
//
// Browsers cannot set the Authorization header on a WebSocket, so the bearer token
// rides in the `bearer.<token>` subprotocol alongside the "harlequin" protocol.
import { getToken, getBase } from "./api";
import type { StreamEvent } from "./types";
import { WS } from "./types";

type StatusHandler = (s: "open" | "reconnecting" | "closed") => void;

function wsUrl(sessionID: number): string {
  const base = getBase() || location.origin;
  const wsBase = base.replace(/^http/, "ws");
  return `${wsBase}/api/v1/sessions/${sessionID}/ws`;
}

export class SessionSocket {
  private ws: WebSocket | null = null;
  private lastSeq = 0;
  private closedByUser = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  private pending: string[] = []; // prompts submitted before the socket was open

  constructor(
    private sessionID: number,
    private onEvent: (ev: StreamEvent) => void,
    private onStatus?: StatusHandler,
  ) {}

  // open connects with have_seq 0 (cold resume); reconnects reuse the last seq.
  open(): void {
    this.connect(this.lastSeq);
  }

  private connect(haveSeq: number): void {
    const protocols = ["harlequin"];
    const tok = getToken();
    if (tok) protocols.push("bearer." + tok);
    const ws = new WebSocket(wsUrl(this.sessionID), protocols);
    this.ws = ws;

    ws.onopen = () => {
      this.onStatus?.("open");
      ws.send(JSON.stringify({ type: WS.Hello, have_seq: haveSeq }));
      // Flush prompts queued while (re)connecting.
      for (const c of this.pending) ws.send(JSON.stringify({ type: WS.Prompt, content: c }));
      this.pending = [];
    };
    ws.onmessage = (e) => {
      let ev: StreamEvent;
      try {
        ev = JSON.parse(e.data);
      } catch {
        return;
      }
      if (typeof ev.seq === "number" && ev.seq > 0) this.lastSeq = ev.seq;
      this.onEvent(ev);
    };
    ws.onclose = () => {
      this.ws = null;
      if (this.closedByUser) {
        this.onStatus?.("closed");
        return;
      }
      // The server-side session keeps running; reconnect and resume from lastSeq.
      this.onStatus?.("reconnecting");
      this.reconnectTimer = setTimeout(() => this.connect(this.lastSeq), 1000);
    };
    ws.onerror = () => {
      /* an onclose follows; reconnect handled there */
    };
  }

  submit(content: string): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: WS.Prompt, content }));
    } else {
      this.pending.push(content);
    }
  }

  interrupt(): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: WS.Interrupt }));
    }
  }

  close(): void {
    this.closedByUser = true;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.ws?.close();
    this.ws = null;
  }
}
