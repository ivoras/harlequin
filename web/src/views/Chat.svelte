<script lang="ts">
  import { session, user, toast } from "../lib/stores";
  import { sc } from "../lib/session.svelte";
  import { api } from "../lib/api";
  import { renderMarkdown } from "../lib/markdown";
  import { matchSlash, runSlash, availableCommands } from "../lib/slash";
  import type { DocChunkInfo } from "../lib/types";

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

  // --- Document citations (d.u.N spans produced by renderMarkdown) ---
  // Lazily resolved and cached; hover sets a tooltip (title + scope + page),
  // click opens the stored original (PDFs anchored to the page) in a new tab.
  const citeCache = new Map<string, Promise<DocChunkInfo>>();
  function resolveCite(cid: string): Promise<DocChunkInfo> {
    let p = citeCache.get(cid);
    if (!p) {
      p = api.getDocChunk(cid, cid.startsWith("d.p.") ? sc.currentProjectID : 0);
      citeCache.set(cid, p);
    }
    return p;
  }
  function citeTarget(e: Event): HTMLElement | null {
    const el = (e.target as HTMLElement)?.closest?.(".cite");
    return el instanceof HTMLElement ? el : null;
  }
  async function onCiteHover(e: Event) {
    const el = citeTarget(e);
    const cid = el?.dataset.cite;
    if (!el || !cid || el.title) return;
    try {
      const info = await resolveCite(cid);
      const page = info.page ? `, p.${info.page}` : "";
      const open = info.has_file ? " — click to open" : "";
      el.title = `${info.title || "untitled"} (${info.scope}${page})${open}`;
      if (info.has_file) el.classList.add("openable");
    } catch {
      el.title = "unknown reference";
    }
  }
  async function onCiteClick(e: Event) {
    const el = citeTarget(e);
    const cid = el?.dataset.cite;
    if (!el || !cid) return;
    try {
      const info = await resolveCite(cid);
      if (!info.has_file) {
        toast(`${info.title || "untitled"} (${info.scope}) — no stored file to open`);
        return;
      }
      const projectID = info.scope === "project" ? sc.currentProjectID : 0;
      const blob = await api.fetchDocumentFile(info.document_id, info.scope, projectID);
      const url = URL.createObjectURL(blob);
      const anchor = info.mime === "application/pdf" && info.page ? `#page=${info.page}` : "";
      window.open(url + anchor, "_blank", "noopener");
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch (err) {
      toast((err as Error).message, "error");
    }
  }

  // --- Whole-document references (p.18, u.4 … spans produced by renderMarkdown) ---
  // Unlike a chunk citation, a doc ref carries no page/chunk to resolve first —
  // scope and id come straight from the ref text. Try the stored original
  // (PDF/DOCX) first; documents with no stored file (e.g. save_doc reports)
  // fall back to their full extracted text, opened as a plain-text tab.
  const DOCREF_SCOPE: Record<string, string> = { u: "personal", p: "project", s: "shared" };
  function docrefTarget(e: Event): HTMLElement | null {
    const el = (e.target as HTMLElement)?.closest?.(".docref");
    return el instanceof HTMLElement ? el : null;
  }
  async function onDocrefClick(e: Event) {
    const el = docrefTarget(e);
    const ref = el?.dataset.docref;
    if (!el || !ref) return;
    const [letter, idStr] = ref.split(".");
    const scope = DOCREF_SCOPE[letter];
    const id = Number(idStr);
    if (!scope || !id) return;
    const projectID = scope === "project" ? sc.currentProjectID : 0;
    try {
      const blob = await api.fetchDocumentFile(id, scope, projectID);
      const url = URL.createObjectURL(blob);
      window.open(url, "_blank", "noopener");
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch {
      try {
        const res = await api.getDocumentContent(id, scope, projectID);
        const blob = new Blob([res.content], { type: "text/plain;charset=utf-8" });
        const url = URL.createObjectURL(blob);
        window.open(url, "_blank", "noopener");
        setTimeout(() => URL.revokeObjectURL(url), 60_000);
      } catch (err) {
        toast((err as Error).message || `couldn't open ${ref}`, "error");
      }
    }
  }

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

  // submit runs a slash command (lines starting with "/") or sends a message.
  async function submit() {
    const text = input.trim();
    if (!text) return;
    if (text.startsWith("/")) {
      input = "";
      slashSel = 0;
      if ((await runSlash(text, admin)) === "help") showHelp = true;
      return;
    }
    if (!$session.id) return;
    input = "";
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
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  }
</script>

<div class="chat">
  <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events, a11y_mouse_events_have_key_events -->
  <div class="messages" bind:this={scrollEl}
    onclick={(e) => { onCiteClick(e); onDocrefClick(e); }} onmouseover={onCiteHover}>
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
        {:else if it.kind === "stats"}
          <div class="muted small stats">{it.text}</div>
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
      <textarea rows="1" placeholder={sc.loading ? "Message (will queue)…" : "Message or /command…"} bind:value={input} bind:this={inputEl} onkeydown={onKey}></textarea>
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
  .messages { flex: 1; min-height: 0; overflow-y: auto; -webkit-overflow-scrolling: touch; padding: 8px 0 12px; }
  .msg.user { display: flex; justify-content: flex-end; }
  .bubble { background: linear-gradient(to bottom, var(--surface-3), var(--surface-2));
    border: 1px solid var(--border-soft); border-radius: 14px 14px 4px 14px;
    padding: 8px 12px; max-width: 85%; white-space: pre-wrap; word-break: break-word;
    box-shadow: var(--hl), var(--shadow-1); }
  .msg.assistant .md { max-width: 100%; word-break: break-word; }
  .md :global(p) { margin: 0.4em 0; }
  .md :global(pre) { white-space: pre; }
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
