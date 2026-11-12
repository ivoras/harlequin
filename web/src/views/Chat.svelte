<script lang="ts">
  import { get } from "svelte/store";
  import { session, user, toast, activeProject } from "../lib/stores";
  import { sc } from "../lib/session.svelte";
  import { renderMarkdown } from "../lib/markdown";
  import { availableCommands } from "../lib/slash";
  import { Composer } from "../lib/composer.svelte";
  import AttachChips from "../lib/AttachChips.svelte";
  import { onCiteHover, onCiteClick, onDocrefClick, onCodeCopy } from "../lib/docview.svelte";

  // Chat is a thin view over the app-scoped session controller (sc), which owns
  // the WebSocket and chat state so it survives view switches. Composer
  // behaviour (slash menu, history recall, submit) lives in lib/composer.
  let scrollEl: HTMLDivElement | undefined;
  const admin = $derived(!!$user && ($user.role === "owner" || $user.role === "admin"));
  const c = new Composer(() => admin);
  function attach() {
    const inp = document.createElement("input");
    inp.type = "file";
    inp.multiple = true;
    inp.onchange = () => {
      const p = get(activeProject);
      if (inp.files) c.attach([...inp.files], p ? { scope: "project", projectID: p.id } : { scope: "personal", projectID: 0 });
    };
    inp.click();
  }

  // Rewind (see SessionController.rewind): a user message goes back into the
  // composer to edit and resend.
  async function rewindTo(i: number, isUser: boolean) {
    const removed = await sc.rewindWithConfirm(i, isUser);
    if (removed !== null && isUser) {
      c.input = removed;
      c.focus();
    }
  }

  // Whole-message copy: copies the raw markdown source, not the rendered HTML.
  let copiedIdx = $state(-1);
  async function copyMsg(i: number, text: string) {
    try {
      await navigator.clipboard.writeText(text);
    } catch (err) {
      toast((err as Error).message || "copy failed", "error");
      return;
    }
    copiedIdx = i;
    setTimeout(() => {
      if (copiedIdx === i) copiedIdx = -1;
    }, 1200);
  }

  function scrollToBottom() {
    requestAnimationFrame(() => {
      if (scrollEl) scrollEl.scrollTop = scrollEl.scrollHeight;
    });
  }

  // Sticky auto-scroll: only follow the transcript while the user is at (or
  // near) the bottom; scrolling up to read older messages disables it until
  // they return. Plain boolean on purpose — it must not retrigger the effect.
  let atBottom = true;
  function onScroll() {
    if (!scrollEl) return;
    atBottom = scrollEl.scrollTop + scrollEl.clientHeight >= scrollEl.scrollHeight - 80;
  }

  // Auto-scroll as the transcript grows (controller-driven).
  $effect(() => {
    sc.items.length;
    if (atBottom) scrollToBottom();
  });

  // When the turn ends on a free-text question (no preset options), focus the
  // composer so the user can answer immediately.
  $effect(() => {
    if (sc.loading) return;
    const last = sc.items.at(-1);
    if (last && last.kind === "ask" && last.options.length === 0) c.focus();
  });
</script>

<div class="chat">
  <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events, a11y_mouse_events_have_key_events -->
  <div class="messages" bind:this={scrollEl} onscroll={onScroll}
    onclick={(e) => { onCiteClick(e); onDocrefClick(e); onCodeCopy(e); }} onmouseover={onCiteHover}>
    <div class="container col" style="gap:12px;">
      {#each sc.items as it, i}
        {#if it.kind === "msg"}
          <div class="msg {it.role}">
            {#if it.role === "assistant"}
              <div class="md">{@html renderMarkdown(it.content)}</div>
              <button class="msgcopy" title="Copy message" aria-label="Copy message"
                onclick={() => copyMsg(i, it.content)}>{copiedIdx === i ? "✓" : "⧉ Copy"}</button>
              {#if sc.rewindTarget(i)}
                <button class="msgcopy" title="Rewind to here: remove everything after this reply" aria-label="Rewind to here"
                  disabled={sc.loading} onclick={() => rewindTo(i, false)}>↶ Rewind</button>
              {/if}
            {:else}
              <div class="bubble">{it.content}</div>
              <button class="msgcopy" title="Rewind: remove this message and everything after it, and edit it again" aria-label="Rewind to before this message"
                disabled={sc.loading} onclick={() => rewindTo(i, true)}>↶ Rewind</button>
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
        {:else if it.kind === "stats"}
          <div class="muted small stats">{it.text}</div>
        {:else if it.kind === "error"}
          <div class="turn-error small">⚠ {it.text}</div>
        {:else if it.kind === "ask"}
          <div class="card col ask">
            <div>{it.question}</div>
            {#if it.options.length}
              <div class="row" style="flex-wrap:wrap; gap:6px;">
                {#each it.options as o}<button class="small" onclick={() => c.answer(o)}>{o}</button>{/each}
              </div>
            {:else}
              <div class="muted small">Type your answer below.</div>
            {/if}
          </div>
        {/if}
      {/each}
      {#if sc.loading && sc.ppLabel}
        <div class="muted small ppline">{sc.ppLabel}</div>
      {/if}
      {#if sc.items.length === 0}
        <div class="muted" style="text-align:center; margin-top:18vh;">Start the session.</div>
      {/if}
    </div>
  </div>

  {#if c.showHelp}
    <div class="scrim" role="presentation" onclick={() => (c.showHelp = false)}></div>
    <aside class="sheet bottom">
      <header><strong>Commands</strong><span class="spacer"></span>
        <button class="ghost" onclick={() => (c.showHelp = false)}>Close</button></header>
      <div class="body list">
        {#each availableCommands(admin) as cmd}
          <div class="row" style="gap:8px; align-items:baseline;">
            <code>{cmd.name}{#if cmd.args} {cmd.args}{/if}</code>
            <span class="muted small">{cmd.desc}</span>
          </div>
        {/each}
        <div class="muted small">Other actions are in the tabs and the ☰ sessions drawer.</div>
      </div>
    </aside>
  {/if}

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
    {#if c.suggestions.length}
      <div class="container col slashmenu" style="gap:2px; padding-bottom:6px;">
        {#each c.suggestions as cmd, i}
          <button class="slashitem row" class:sel={i === c.slashSel}
            onmouseenter={() => (c.slashSel = i)} onclick={c.completeSlash}>
            <code>{cmd.name}{#if cmd.args} {cmd.args}{/if}</code>
            <span class="muted small">{cmd.desc}</span>
          </button>
        {/each}
      </div>
    {/if}
    <div class="container"><AttachChips {c} /></div>
    <div class="container row" style="align-items:flex-end; gap:8px;">
      <button onclick={attach} disabled={sc.missing} title="Attach documents" aria-label="Attach documents">📎</button>
      <textarea rows="1" placeholder={sc.loading ? "Message (will queue)…" : "Message or /command…"} bind:value={c.input} bind:this={c.el} onkeydown={c.onKey} oninput={c.onInput} disabled={sc.missing}></textarea>
      {#if sc.loading}
        <button class="danger" onclick={() => sc.stop()} title="Stop">■</button>
      {:else}
        <button class="primary" onclick={() => c.submit()} disabled={sc.missing || c.uploading || !c.input.trim() || (!$session.id && !c.input.trim().startsWith("/"))}>Send</button>
      {/if}
    </div>
    {#if sc.missing}
      <div class="container small" style="padding-top:2px; color: var(--danger, #f66);">This session wasn't found here (deleted or moved) — start a new one or pick another.</div>
    {:else if sc.reconnecting}
      <div class="container muted small" style="padding-top:2px;">Reconnecting…</div>
    {/if}
  </div>
</div>

<style>
  .chat { flex: 1; min-height: 0; display: flex; flex-direction: column; }
  .turn-error { color: var(--danger, #e5484d); border: 1px solid color-mix(in srgb, currentColor 35%, transparent);
    border-radius: var(--radius-sm); padding: 6px 10px; background: color-mix(in srgb, currentColor 8%, transparent); }
  .messages { flex: 1; min-height: 0; overflow-y: auto; -webkit-overflow-scrolling: touch; padding: 8px 0 12px; }
  .msg.user { display: flex; flex-direction: column; align-items: flex-end; }
  .bubble { background: linear-gradient(to bottom, var(--surface-3), var(--surface-2));
    border: 1px solid var(--border-soft); border-radius: 14px 14px 4px 14px;
    padding: 8px 12px; max-width: 85%; white-space: pre-wrap; word-break: break-word;
    box-shadow: var(--hl), var(--shadow-1); }
  .msg.assistant .md { max-width: 100%; word-break: break-word; }
  .md :global(p) { margin: 0.4em 0; }
  .md :global(pre) { white-space: pre; }
  /* Per-code-block copy button (markup injected by renderMarkdown). */
  .md :global(.codewrap) { position: relative; }
  .md :global(.codecopy) { position: absolute; top: 4px; right: 4px; padding: 1px 8px;
    font-size: 12px; line-height: 1.5; background: var(--surface-2);
    border: 1px solid var(--border-soft); box-shadow: none; opacity: 0.6; }
  .md :global(.codecopy:hover) { opacity: 1; }
  /* Whole-message copy: subtle, always visible (no hover-gating for touch). */
  .msgcopy { display: inline-block; margin-top: 2px; padding: 1px 8px; font-size: 12px;
    background: transparent; border: 1px solid var(--border-soft); box-shadow: none;
    color: var(--muted, inherit); opacity: 0.6; }
  .msgcopy:hover { opacity: 1; }
  .thinking { background: var(--surface); border: 1px solid var(--border-soft);
    border-radius: var(--radius-sm); padding: 6px 10px; box-shadow: var(--hl); }
  .thinking-body { white-space: pre-wrap; margin-top: 6px; }
  .tool { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .tool .args { max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tool-out { white-space: pre-wrap; width: 100%; max-height: 9em; overflow-y: auto; }
  .composer { border-top: 1px solid var(--border); background: var(--surface);
    box-shadow: 0 -2px 12px rgba(0, 0, 0, 0.25); position: relative; z-index: 5;
    padding: 8px 0 max(8px, env(safe-area-inset-bottom)); }
  textarea { resize: none; max-height: 40dvh; }
  .slashmenu { max-height: 40dvh; overflow-y: auto; }
  .slashitem { width: 100%; justify-content: flex-start; gap: 10px; text-align: left;
    background: var(--surface-2); border: 1px solid transparent; box-shadow: none; }
  .slashitem.sel { border-color: var(--accent-dim); background: var(--surface-3);
    box-shadow: var(--hl), var(--shadow-1); }
</style>
