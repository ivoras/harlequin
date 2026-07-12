<script lang="ts">
  import { session, user, toast } from "../lib/stores";
  import { sc } from "../lib/session.svelte";
  import { renderMarkdown } from "../lib/markdown";
  import { matchSlash, runSlash, availableCommands } from "../lib/slash";
  import { onCiteHover, onCiteClick, onDocrefClick, onCodeCopy } from "../lib/docview.svelte";

  // Chat is a thin view over the app-scoped session controller (sc), which owns
  // the WebSocket and chat state so it survives view switches. Only view-local
  // concerns (composer text, scrolling, focus, slash commands) live here.
  let input = $state("");
  let scrollEl: HTMLDivElement | undefined;
  let inputEl: HTMLTextAreaElement | undefined;

  // Slash-command autocomplete + help, mirroring the TUI.
  let slashSel = $state(0);
  let showHelp = $state(false);
  const admin = $derived(!!$user && ($user.role === "owner" || $user.role === "admin"));
  const suggestions = $derived(matchSlash(input, admin));
  // Keep the highlighted suggestion in range as the list narrows.
  $effect(() => {
    if (slashSel >= suggestions.length) slashSel = 0;
  });

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
    if (last && last.kind === "ask" && last.options.length === 0) inputEl?.focus();
  });

  // Auto-grow the composer with its content; the CSS max-height caps it.
  // Called from oninput and explicitly after programmatic clears, since
  // setting `input` from code doesn't fire the input event.
  function resizeInput() {
    // rAF so programmatic `input` changes have reached the DOM before measuring.
    requestAnimationFrame(() => {
      if (!inputEl) return;
      inputEl.style.height = "auto";
      inputEl.style.height = `${inputEl.scrollHeight}px`;
    });
  }

  // --- Input history recall ---
  // ArrowUp in an empty (or unmodified-recall) composer walks back through
  // previous user messages for editing/resend; ArrowDown walks toward
  // newest/empty. The cursor resets whenever the user types normally.
  let histIdx = -1; // -1 = not recalling; otherwise index into userHistory() (0 = newest)
  let histRecalled = ""; // exact text last inserted by recall, to detect edits
  function userHistory(): string[] {
    const out: string[] = [];
    for (const it of sc.items) if (it.kind === "msg" && it.role === "user") out.push(it.content);
    return out.reverse();
  }
  function recall(delta: number) {
    const hist = userHistory();
    const next = histIdx + delta;
    if (next < -1 || next >= hist.length) return;
    histIdx = next;
    histRecalled = next === -1 ? "" : hist[next];
    input = histRecalled;
    resizeInput();
  }
  function resetHistory() {
    histIdx = -1;
    histRecalled = "";
  }
  function onInput(e: Event) {
    // Any edit that diverges from the recalled text ends the recall session.
    if ((e.currentTarget as HTMLTextAreaElement).value !== histRecalled) resetHistory();
    resizeInput();
  }

  // submit runs a slash command (lines starting with "/") or sends a message.
  async function submit() {
    const text = input.trim();
    if (!text) return;
    resetHistory();
    if (text.startsWith("/")) {
      input = "";
      slashSel = 0;
      resizeInput();
      if ((await runSlash(text, admin)) === "help") showHelp = true;
      return;
    }
    if (!$session.id) return;
    input = "";
    resizeInput();
    sc.send(text);
  }
  function answerAsk(opt: string) {
    input = opt;
    submit();
  }
  function completeSlash() {
    if (suggestions.length) {
      input = suggestions[slashSel].name + " ";
      slashSel = 0;
      inputEl?.focus();
    }
  }
  function onKey(e: KeyboardEvent) {
    if (suggestions.length) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        slashSel = Math.min(slashSel + 1, suggestions.length - 1);
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        slashSel = Math.max(slashSel - 1, 0);
        return;
      }
      if (e.key === "Tab") {
        e.preventDefault();
        completeSlash();
        return;
      }
      // Enter accepts the highlighted suggestion, unless the typed text is
      // already exactly that command — then fall through and run it.
      if (e.key === "Enter" && !e.shiftKey && input.trim() !== suggestions[slashSel].name) {
        e.preventDefault();
        completeSlash();
        return;
      }
    }
    // History recall — only when the slash menu isn't consuming the arrows,
    // and only from an empty composer or an unmodified recall, so normal
    // multiline cursor movement in typed text still works.
    const recalling = histIdx >= 0 && input === histRecalled;
    if (e.key === "ArrowUp" && (input === "" || recalling)) {
      e.preventDefault();
      if (input === "" && !recalling) resetHistory(); // stale cursor after a programmatic clear
      recall(1);
      return;
    }
    if (e.key === "ArrowDown" && recalling) {
      e.preventDefault();
      recall(-1);
      return;
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  }
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
        {:else if it.kind === "stats"}
          <div class="muted small stats">{it.text}</div>
        {:else if it.kind === "error"}
          <div class="turn-error small">⚠ {it.text}</div>
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
      {#if sc.loading && sc.ppLabel}
        <div class="muted small ppline">{sc.ppLabel}</div>
      {/if}
      {#if sc.items.length === 0}
        <div class="muted" style="text-align:center; margin-top:18vh;">Start the session.</div>
      {/if}
    </div>
  </div>

  {#if showHelp}
    <div class="scrim" role="presentation" onclick={() => (showHelp = false)}></div>
    <aside class="sheet bottom">
      <header><strong>Commands</strong><span class="spacer"></span>
        <button class="ghost" onclick={() => (showHelp = false)}>Close</button></header>
      <div class="body list">
        {#each availableCommands(admin) as c}
          <div class="row" style="gap:8px; align-items:baseline;">
            <code>{c.name}{#if c.args} {c.args}{/if}</code>
            <span class="muted small">{c.desc}</span>
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
    {#if suggestions.length}
      <div class="container col slashmenu" style="gap:2px; padding-bottom:6px;">
        {#each suggestions as c, i}
          <button class="slashitem row" class:sel={i === slashSel}
            onmouseenter={() => (slashSel = i)} onclick={completeSlash}>
            <code>{c.name}{#if c.args} {c.args}{/if}</code>
            <span class="muted small">{c.desc}</span>
          </button>
        {/each}
      </div>
    {/if}
    <div class="container row" style="align-items:flex-end; gap:8px;">
      <textarea rows="1" placeholder={sc.loading ? "Message (will queue)…" : "Message or /command…"} bind:value={input} bind:this={inputEl} onkeydown={onKey} oninput={onInput}></textarea>
      {#if sc.loading}
        <button class="danger" onclick={() => sc.stop()} title="Stop">■</button>
      {:else}
        <button class="primary" onclick={submit} disabled={!input.trim() || (!$session.id && !input.trim().startsWith("/"))}>Send</button>
      {/if}
    </div>
    {#if sc.reconnecting}
      <div class="container muted small" style="padding-top:2px;">Reconnecting…</div>
    {/if}
  </div>
</div>

<style>
  .chat { flex: 1; min-height: 0; display: flex; flex-direction: column; }
  .turn-error { color: var(--danger, #e5484d); border: 1px solid color-mix(in srgb, currentColor 35%, transparent);
    border-radius: var(--radius-sm); padding: 6px 10px; background: color-mix(in srgb, currentColor 8%, transparent); }
  .messages { flex: 1; min-height: 0; overflow-y: auto; -webkit-overflow-scrolling: touch; padding: 8px 0 12px; }
  .msg.user { display: flex; justify-content: flex-end; }
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
