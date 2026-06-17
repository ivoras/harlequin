<script lang="ts">
  import { session } from "../lib/stores";
  import { sc } from "../lib/session.svelte";
  import { renderMarkdown } from "../lib/markdown";

  // Chat is a thin view over the app-scoped session controller (sc), which owns
  // the WebSocket and chat state so it survives view switches. Only view-local
  // concerns (composer text, scrolling, focus) live here.
  let input = $state("");
  let scrollEl: HTMLDivElement | undefined;
  let inputEl: HTMLTextAreaElement | undefined;

  function scrollToBottom() {
    requestAnimationFrame(() => {
      if (scrollEl) scrollEl.scrollTop = scrollEl.scrollHeight;
    });
  }

  // Auto-scroll as the transcript grows (controller-driven).
  $effect(() => {
    sc.items.length;
    scrollToBottom();
  });

  // When the turn ends on a free-text question (no preset options), focus the
  // composer so the user can answer immediately.
  $effect(() => {
    if (sc.loading) return;
    const last = sc.items.at(-1);
    if (last && last.kind === "ask" && last.options.length === 0) inputEl?.focus();
  });

  function send() {
    const text = input.trim();
    if (!text || !$session.id) return;
    input = "";
    sc.send(text);
  }
  function answerAsk(opt: string) {
    input = opt;
    send();
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
      {#each sc.items as it}
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
      {#if sc.items.length === 0}
        <div class="muted" style="text-align:center; margin-top:18vh;">Start the session.</div>
      {/if}
    </div>
  </div>

  <div class="composer">
    {#if sc.queue.length}
      <div class="container col" style="gap:4px; padding-bottom:6px;">
        <div class="muted small">Queued ({sc.queue.length}) — sent in order as the agent frees up:</div>
        {#each sc.queue as q, i}
          <div class="row queued" style="gap:6px; align-items:center;">
            <span class="small wrap" style="flex:1; min-width:0;">{i + 1}. {q}</span>
            <button class="ghost danger small" onclick={() => sc.removeQueued(i)} aria-label="Remove">✕</button>
          </div>
        {/each}
      </div>
    {/if}
    <div class="container row" style="align-items:flex-end; gap:8px;">
      <textarea rows="1" placeholder={sc.loading ? "Message (will queue)…" : "Message…"} bind:value={input} bind:this={inputEl} onkeydown={onKey}></textarea>
      {#if sc.loading}
        <button class="danger" onclick={() => sc.stop()} title="Stop">■</button>
      {:else}
        <button class="primary" onclick={send} disabled={!input.trim() || !$session.id}>Send</button>
      {/if}
    </div>
    {#if sc.reconnecting}
      <div class="container muted small" style="padding-top:2px;">Reconnecting…</div>
    {:else if sc.loading && sc.ppLabel}
      <div class="container muted small" style="padding-top:2px;">{sc.ppLabel}</div>
    {:else if sc.ctx}
      <div class="container muted small" style="padding-top:2px;">
        {sc.ctx.model}{#if sc.ctx.max} · {fmtk(sc.ctx.used)}/{fmtk(sc.ctx.max)} ctx{/if}
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
