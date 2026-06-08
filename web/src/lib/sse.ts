// Streaming chat: POST a message and read the SSE response. EventSource can't do
// POST, so we use fetch + a ReadableStream reader and parse `data:` frames
// ourselves. Returns when the stream ends (a `done` event or EOF). Abort via the
// AbortSignal (the Stop button).
import { apiUrl, authHeaders } from "./api";
import type { StreamEvent } from "./types";
import { SSE } from "./types";

export async function streamMessage(
  conversationID: number,
  content: string,
  onEvent: (ev: StreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(apiUrl(`/conversations/${conversationID}/messages`), {
    method: "POST",
    headers: authHeaders({ "Content-Type": "application/json", Accept: "text/event-stream" }),
    body: JSON.stringify({ content }),
    signal,
  });
  if (!res.ok || !res.body) {
    let msg = res.status + " " + res.statusText;
    try {
      const e = await res.json();
      if (e && e.error) msg = e.error;
    } catch { /* keep status */ }
    throw new Error(msg);
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      // SSE frames are separated by a blank line.
      let sep: number;
      while ((sep = buf.indexOf("\n\n")) >= 0) {
        const frame = buf.slice(0, sep);
        buf = buf.slice(sep + 2);
        const ev = parseFrame(frame);
        if (ev) {
          onEvent(ev);
          if (ev.type === SSE.Done) return;
        }
      }
    }
  } finally {
    try {
      await reader.cancel();
    } catch { /* ignore */ }
  }
}

function parseFrame(frame: string): StreamEvent | null {
  // A frame may contain multiple `data:` lines (concatenated payload).
  const data = frame
    .split("\n")
    .filter((l) => l.startsWith("data:"))
    .map((l) => l.slice(5).replace(/^ /, ""))
    .join("");
  if (!data) return null;
  try {
    return JSON.parse(data) as StreamEvent;
  } catch {
    return null;
  }
}
