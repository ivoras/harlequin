<script lang="ts">
  import { session, toast } from "../lib/stores";
  import { api } from "../lib/api";
  import { streamMessage } from "../lib/sse";
  import { renderMarkdown } from "../lib/markdown";
  import { SSE } from "../lib/types";
  import type { Message, StreamEvent } from "../lib/types";

  type Item =
    | { kind: "msg"; role: "user" | "assistant"; content: string }
    | { kind: "thinking"; text: string }
    | { kind: "tool"; name: string; args: string; output: string; ms: number; done: boolean }
    | { kind: "ask"; question: string; options: string[] };

  let items = $state<Item[]>([]);
  let input = $state("");
  let loading = $state(false);
  let ctx = $state<{ model: string; used: number; max: number } | null>(null);
  let abort: AbortController | null = null;
  let loadedFor = 0;
  let scrollEl: HTMLDivElement | undefined;

  // Load history whenever the active conversation changes (not on each message).
  $effect(() => {
    const id = $session.id;
    if (id && id !== loadedFor) {
      loadedFor = id;
      loadHistory(id);
    }
  });

  async function loadHistory(id: number) {
    items = [];
    ctx = null;
    try {
      const msgs = await api.getMessages(id);
      items = msgs.flatMap(toItems);
      scrollToBottom();
    } catch (e) {
      toast((e as Error).message, "error");
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

  function scrollToBottom() {
    requestAnimationFrame(() => {
      if (scrollEl) scrollEl.scrollTop = scrollEl.scrollHeight;
    });
  }

  async function send() {
    const text = input.trim();
    if (!text || loading || !$session.id) return;
    input = "";
    items.push({ kind: "msg", role: "user", content: text });
    loading = true;
    let assistant: Item | null = null;
    const getAssistant = (): Item => {
      if (!assistant) {
        items.push({ kind: "msg", role: "assistant", content: "" });
        assistant = items[items.length - 1];
      }
      return assistant;
    };
    abort = new AbortController();
    scrollToBottom();
    try {
      await streamMessage(
        $session.id,
        text,
        (ev) => {
          handleEvent(ev, getAssistant);
          scrollToBottom();
        },
        abort.signal,
      );
    } catch (e) {
      const err = e as Error;
      if (err.name !== "AbortError") toast(err.message, "error");
    } finally {
      loading = false;
      abort = null;
    }
  }

  function handleEvent(ev: StreamEvent, getAssistant: () => Item) {
    switch (ev.type) {
      case SSE.Token: {
        const a = getAssistant();
        if (a.kind === "msg") a.content += ev.text || "";
        break;
      }
      case SSE.Thinking: {
        let last = items.at(-1);
        if (!last || last.kind !== "thinking") {
          items.push({ kind: "thinking", text: "" });
          last = items.at(-1);
        }
        if (last && last.kind === "thinking") last.text += ev.thinking || "";
        break;
      }
      case SSE.ToolCall:
        items.push({ kind: "tool", name: ev.tool_name || "tool", args: ev.tool_args || "", output: "", ms: 0, done: false });
        break;
      case SSE.ToolResult:
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
      case SSE.Error:
        toast(ev.error || "error", "error");
        break;
      case SSE.Done:
        if (ev.model) ctx = { model: ev.model, used: ev.context_tokens || 0, max: ev.context_max || 0 };
        break;
    }
  }

  function answerAsk(opt: string) {
    input = opt;
    send();
  }
  function stop() {
    abort?.abort();
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
            {/if}
          </div>
        {/if}
      {/each}
      {#if items.length === 0}
        <div class="muted" style="text-align:center; margin-top:18vh;">Start the conversation.</div>
      {/if}
    </div>
  </div>

  <div class="composer">
    <div class="container row" style="align-items:flex-end; gap:8px;">
      <textarea rows="1" placeholder="Message…" bind:value={input} onkeydown={onKey}></textarea>
      {#if loading}
        <button class="danger" onclick={stop} title="Stop">■</button>
      {:else}
        <button class="primary" onclick={send} disabled={!input.trim() || !$session.id}>Send</button>
      {/if}
    </div>
    {#if ctx}
      <div class="container muted small" style="padding-top:2px;">
        {ctx.model}{#if ctx.max} · {fmtk(ctx.used)}/{fmtk(ctx.max)} ctx{/if}
      </div>
    {/if}
  </div>
</div>

<style>
  .chat { flex: 1; min-height: 0; display: flex; flex-direction: column; }
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
