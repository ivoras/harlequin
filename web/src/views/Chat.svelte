<script lang="ts">
  import { session, toast } from "../lib/stores";
  import { api } from "../lib/api";
  import { SessionSocket } from "../lib/ws";
  import { renderMarkdown } from "../lib/markdown";
  import { SSE, NOTIFY_SESSION_TITLE } from "../lib/types";
  import type { Message, StreamEvent, Notification } from "../lib/types";

  type Item =
    | { kind: "msg"; role: "user" | "assistant"; content: string }
    | { kind: "thinking"; text: string }
    | { kind: "tool"; name: string; args: string; output: string; ms: number; done: boolean }
    | { kind: "ask"; question: string; options: string[] };

  let items = $state<Item[]>([]);
  let input = $state("");
  let loading = $state(false);
  let ppLabel = $state(""); // live prompt-processing progress, before the first token
  let queue = $state<string[]>([]); // messages typed while a turn is in flight
  let ctx = $state<{ model: string; used: number; max: number } | null>(null);
  let reconnecting = $state(false);
  // Active server alerts shown in a persistent box above the transcript (not part
  // of the session); kept until dismissed.
  let alerts = $state<Notification[]>([]);
  let loadedFor = 0;
  let scrollEl: HTMLDivElement | undefined;
  let inputEl: HTMLTextAreaElement | undefined;

  // The live session socket and resume bookkeeping.
  let socket: SessionSocket | null = null;
  let optimisticUser = 0; // user-message echoes to skip (we rendered them locally)
  let coldHistory: Message[] | null = null; // committed history awaiting the synced cut
  let streamingAssistant: Item | null = null; // current in-flight assistant bubble

  // When the turn ends on a free-text question (no preset options), focus the
  // composer so the user can answer immediately — no extra click needed.
  $effect(() => {
    if (loading) return;
    const last = items.at(-1);
    if (last && last.kind === "ask" && last.options.length === 0) inputEl?.focus();
  });

  // (Re)attach whenever the active session changes: load committed history, then
  // open the socket so any in-flight turn replays and continues live.
  $effect(() => {
    const id = $session.id;
    if (id && id !== loadedFor) {
      loadedFor = id;
      resume(id);
    }
  });

  async function resume(id: number) {
    items = [];
    ctx = null;
    loading = false;
    optimisticUser = 0;
    streamingAssistant = null;
    queue = [];
    socket?.close();
    try {
      // Load committed history first so it is in hand when the synced frame tells
      // us where the in-flight turn (if any) begins.
      coldHistory = await api.getMessages(id);
    } catch (e) {
      coldHistory = [];
      toast((e as Error).message, "error");
    }
    if ($session.id !== id) return; // switched again while loading
    socket = new SessionSocket(id, handleEvent, (s) => (reconnecting = s === "reconnecting"));
    socket.open();
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

  function scrollToBottom() {
    requestAnimationFrame(() => {
      if (scrollEl) scrollEl.scrollTop = scrollEl.scrollHeight;
    });
  }

  // send() is the entry point from the composer. While a turn is in flight the
  // message is queued (and shown) instead of starting a second concurrent turn;
  // the queue drains in order as each turn finishes.
  function send() {
    const text = input.trim();
    if (!text || !$session.id || !socket) return;
    input = "";
    if (loading) {
      queue.push(text);
      return;
    }
    sendText(text);
  }
  function removeQueued(i: number) {
    queue.splice(i, 1);
  }

  function sendText(text: string) {
    items.push({ kind: "msg", role: "user", content: text });
    optimisticUser++; // skip the server's echo of this prompt
    loading = true;
    streamingAssistant = null;
    socket?.submit(text);
    scrollToBottom();
  }

  function getAssistant(): Item {
    if (!streamingAssistant) {
      items.push({ kind: "msg", role: "assistant", content: "" });
      streamingAssistant = items[items.length - 1];
    }
    return streamingAssistant;
  }

  function handleEvent(ev: StreamEvent) {
    switch (ev.type) {
      case SSE.Synced: {
        // Resume handshake. Cold (we hold coldHistory): render committed history —
        // trimmed to before the in-flight turn when one is running — then the
        // replayed buffer reconstructs that turn. Warm reconnects carry no history.
        if (coldHistory) {
          const through = ev.committed_through || 0;
          const msgs = ev.running ? coldHistory.filter((m) => m.id <= through) : coldHistory;
          items = msgs.flatMap(toItems);
          loading = !!ev.running;
          streamingAssistant = null;
          coldHistory = null;
          scrollToBottom();
        }
        break;
      }
      case SSE.UserMessage: {
        if (optimisticUser > 0) {
          optimisticUser--;
        } else {
          items.push({ kind: "msg", role: "user", content: ev.text || "" });
          loading = true;
          streamingAssistant = null;
        }
        break;
      }
      case SSE.PromptProgress: {
        const total = ev.prompt_total || 0;
        if (total > 0) {
          const pct = Math.floor(((ev.prompt_processed || 0) * 100) / total);
          const label = ev.source ? `${ev.source}: processing prompt` : "Processing prompt";
          ppLabel = `${label} ${pct}% (${ev.prompt_processed}/${total} tok)`;
        }
        break;
      }
      case SSE.Token: {
        ppLabel = ""; // prefill done once tokens flow
        const a = getAssistant();
        if (a.kind === "msg") a.content += ev.text || "";
        break;
      }
      case SSE.Thinking: {
        ppLabel = "";
        let last = items.at(-1);
        if (!last || last.kind !== "thinking") {
          items.push({ kind: "thinking", text: "" });
          last = items.at(-1);
        }
        if (last && last.kind === "thinking") last.text += ev.thinking || "";
        break;
      }
      case SSE.ToolCall:
        streamingAssistant = null; // a fresh assistant bubble follows the tool
        items.push({ kind: "tool", name: ev.tool_name || "tool", args: ev.tool_args || "", output: "", ms: 0, done: false });
        break;
      case SSE.ToolResult:
        ppLabel = ""; // clear any delegated (e.g. WebFetch) prefill progress
        for (let i = items.length - 1; i >= 0; i--) {
          const it = items[i];
          if (it.kind === "tool" && !it.done) {
            it.output = ev.output || "";
            it.ms = ev.duration_ms || 0;
            it.done = true;
            break;
          }
        }
        break;
      case SSE.AskUser:
        items.push({ kind: "ask", question: ev.text || "", options: ev.options || [] });
        break;
      case SSE.Notification: {
        const n = ev.notification;
        if (!n) break;
        if (n.kind === NOTIFY_SESSION_TITLE) {
          if (n.session_id === $session.id) session.update((s) => ({ ...s, title: n.title }));
          api.ackNotification(n.id);
        } else if (!n.auto_run) {
          // Passive notification: show it in the persistent alert box (deduped),
          // kept until the user dismisses it.
          if (!alerts.some((a) => a.id === n.id)) alerts.push(n);
        }
        // auto_run notifications are left for a client that runs them.
        break;
      }
      case SSE.Error:
        toast(ev.error || "error", "error");
        break;
      case SSE.Done:
        if (ev.model) ctx = { model: ev.model, used: ev.context_tokens || 0, max: ev.context_max || 0 };
        loading = false;
        ppLabel = "";
        streamingAssistant = null;
        // Drain the next queued message, if any.
        if (queue.length > 0) {
          const next = queue.shift()!;
          sendText(next);
        }
        break;
    }
    scrollToBottom();
  }

  function answerAsk(opt: string) {
    input = opt;
    send();
  }
  function stop() {
    socket?.interrupt();
    loading = false;
  }
  function dismissAlert(a: Notification) {
    alerts = alerts.filter((x) => x.id !== a.id);
    api.ackNotification(a.id);
  }
  function runAlert(a: Notification) {
    dismissAlert(a);
    if (a.prompt && socket && !loading) sendText(a.prompt);
  }
  function onKey(e: KeyboardEvent) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  }
  const fmtk = (n: number) => (n >= 1000 ? Math.round(n / 1000) + "k" : "" + n);
</script>

<div class="chat">
  {#if alerts.length}
    <div class="alerts">
      <div class="container col" style="gap:6px;">
        {#each alerts as a (a.id)}
          <div class="alert row">
            <span class="bell">🔔</span>
            <div class="col" style="flex:1; min-width:0; gap:2px;">
              <strong class="wrap">{a.title}</strong>
              {#if a.description}<span class="small wrap">{a.description}</span>{/if}
            </div>
            {#if a.prompt}
              <button class="small" onclick={() => runAlert(a)}>Run</button>
            {/if}
            <button class="ghost small" onclick={() => dismissAlert(a)} aria-label="Dismiss">✕</button>
          </div>
        {/each}
      </div>
    </div>
  {/if}
  <div class="messages" bind:this={scrollEl}>
    <div class="container col" style="gap:12px;">
      {#each items as it}
        {#if it.kind === "msg"}
          <div class="msg {it.role}">
            {#if it.role === "assistant"}
              <div class="md">{@html renderMarkdown(it.content)}</div>
            {:else}
              <div class="bubble">{it.content}</div>
            {/if}
          </div>
        {:else if it.kind === "thinking"}
          <details class="thinking">
            <summary class="muted small">Thinking…</summary>
            <div class="muted small thinking-body">{it.text}</div>
          </details>
        {:else if it.kind === "tool"}
          <div class="tool small">
            <span class="pill">{it.name}</span>
            {#if it.args}<code class="args">{it.args}</code>{/if}
            {#if it.done}
              <div class="muted tool-out">{it.output}{#if it.ms} · {it.ms}ms{/if}</div>
            {:else}
              <span class="muted">running…</span>
            {/if}
          </div>
        {:else if it.kind === "ask"}
          <div class="card col ask">
            <div>{it.question}</div>
            {#if it.options.length}
              <div class="row" style="flex-wrap:wrap; gap:6px;">
                {#each it.options as o}<button class="small" onclick={() => answerAsk(o)}>{o}</button>{/each}
              </div>
            {:else}
              <div class="muted small">Type your answer below.</div>
            {/if}
          </div>
        {/if}
      {/each}
      {#if items.length === 0}
        <div class="muted" style="text-align:center; margin-top:18vh;">Start the session.</div>
      {/if}
    </div>
  </div>

  <div class="composer">
    {#if queue.length}
      <div class="container col" style="gap:4px; padding-bottom:6px;">
        <div class="muted small">Queued ({queue.length}) — sent in order as the agent frees up:</div>
        {#each queue as q, i}
          <div class="row queued" style="gap:6px; align-items:center;">
            <span class="small wrap" style="flex:1; min-width:0;">{i + 1}. {q}</span>
            <button class="ghost danger small" onclick={() => removeQueued(i)} aria-label="Remove">✕</button>
          </div>
        {/each}
      </div>
    {/if}
    <div class="container row" style="align-items:flex-end; gap:8px;">
      <textarea rows="1" placeholder={loading ? "Message (will queue)…" : "Message…"} bind:value={input} bind:this={inputEl} onkeydown={onKey}></textarea>
      {#if loading}
        <button class="danger" onclick={stop} title="Stop">■</button>
      {:else}
        <button class="primary" onclick={send} disabled={!input.trim() || !$session.id}>Send</button>
      {/if}
    </div>
    {#if reconnecting}
      <div class="container muted small" style="padding-top:2px;">Reconnecting…</div>
    {:else if loading && ppLabel}
      <div class="container muted small" style="padding-top:2px;">{ppLabel}</div>
    {:else if ctx}
      <div class="container muted small" style="padding-top:2px;">
        {ctx.model}{#if ctx.max} · {fmtk(ctx.used)}/{fmtk(ctx.max)} ctx{/if}
      </div>
    {/if}
  </div>
</div>

<style>
  .chat { flex: 1; min-height: 0; display: flex; flex-direction: column; }
  /* Persistent alert box: pinned above the transcript, does not scroll away. */
  .alerts { flex: none; border-bottom: 1px solid var(--border); background: var(--surface);
    padding: 6px 0; max-height: 30dvh; overflow-y: auto; }
  .alert { align-items: center; gap: 8px; padding: 6px 10px; border-radius: 10px;
    background: var(--surface-3); border: 1px solid var(--accent); }
  .alert .bell { flex: none; }
  .alert .wrap { word-break: break-word; }
  .messages { flex: 1; min-height: 0; overflow-y: auto; -webkit-overflow-scrolling: touch; padding: 8px 0 12px; }
  .msg.user { display: flex; justify-content: flex-end; }
  .bubble { background: var(--surface-3); border: 1px solid var(--border); border-radius: 14px 14px 4px 14px;
    padding: 8px 12px; max-width: 85%; white-space: pre-wrap; word-break: break-word; }
  .msg.assistant .md { max-width: 100%; word-break: break-word; }
  .md :global(p) { margin: 0.4em 0; }
  .md :global(pre) { white-space: pre; }
  .thinking { background: var(--surface); border: 1px solid var(--border); border-radius: 10px; padding: 6px 10px; }
  .thinking-body { white-space: pre-wrap; margin-top: 6px; }
  .tool { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .tool .args { max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tool-out { white-space: pre-wrap; width: 100%; max-height: 9em; overflow-y: auto; }
  .composer { border-top: 1px solid var(--border); background: var(--surface);
    padding: 8px 0 max(8px, env(safe-area-inset-bottom)); }
  textarea { resize: none; max-height: 40dvh; }
</style>
