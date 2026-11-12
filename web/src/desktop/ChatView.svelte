<script lang="ts">
  import { api } from "../lib/api";
  import { session, user, wornHat, activeProject, toast } from "../lib/stores";
  import { sc } from "../lib/session.svelte";
  import { renderMarkdown } from "../lib/markdown";
  import { availableCommands } from "../lib/slash";
  import { Composer } from "../lib/composer.svelte";
  import AttachChips from "../lib/AttachChips.svelte";
  import { onCiteHover, onCiteClick, onDocrefClick, onCodeCopy } from "../lib/docview.svelte";
  import { isElevated, type Hat } from "../lib/types";
  import Icon from "./Icon.svelte";
  import TopBar from "./TopBar.svelte";
  import { ui, MOD } from "./state.svelte";

  const admin = $derived(isElevated($user?.role));
  const c = new Composer(() => admin);

  let scrollEl: HTMLDivElement | undefined;
  let atBottom = true;
  let copiedIdx = $state(-1);

  // --- title (inline rename; project sessions are renamed by their owner in
  // their own workspace, mirroring the mobile UI) ---
  let editingTitle = $state(false);
  let titleDraft = $state("");
  function startRename() {
    if ($activeProject || !$session.id) return;
    titleDraft = $session.title;
    editingTitle = true;
  }
  async function commitRename() {
    if (!editingTitle) return;
    editingTitle = false;
    const t = titleDraft.trim();
    if (!t || t === $session.title) return;
    try {
      await api.renameSession($session.id, t);
      session.set({ id: $session.id, title: t });
      const row = ui.sessions.find((x) => x.id === $session.id);
      if (row) row.title = t;
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  function focusSelect(node: HTMLInputElement) {
    node.focus();
    node.select();
  }

  // --- hat picker ---
  let hatMenu = $state(false);
  let hats = $state<Hat[]>([]);
  async function openHats() {
    hatMenu = !hatMenu;
    if (hatMenu) {
      try {
        hats = await api.listHats();
      } catch (e) {
        toast((e as Error).message, "error");
      }
    }
  }
  async function wear(name: string) {
    hatMenu = false;
    if (!$session.id) return;
    try {
      await api.setSessionHat($session.id, name);
      wornHat.set(name);
      toast(name ? `wearing the ${name} hat` : "hat off");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  async function clearContext() {
    if (!confirm("Clear this session's context? The transcript is wiped; the session, its title and hat are kept.")) return;
    await sc.clear();
  }

  // Rewind the session. On a user message the message itself is removed too and
  // its text returns to the composer for editing; on a reply, the turn is kept.
  async function rewindTo(i: number, isUser: boolean) {
    const removed = await sc.rewindWithConfirm(i, isUser);
    if (removed !== null && isUser) {
      c.input = removed; // as sent, incl. any "Attached documents" references
      c.focus();
    }
  }

  async function copyMsg(i: number, text: string) {
    try {
      await navigator.clipboard.writeText(text);
    } catch (err) {
      toast((err as Error).message || "copy failed", "error");
      return;
    }
    copiedIdx = i;
    setTimeout(() => copiedIdx === i && (copiedIdx = -1), 1200);
  }

  function onScroll() {
    if (scrollEl) atBottom = scrollEl.scrollTop + scrollEl.clientHeight >= scrollEl.scrollHeight - 80;
  }
  // Sticky auto-scroll while the user is at the bottom.
  $effect(() => {
    sc.items.length;
    if (atBottom) requestAnimationFrame(() => scrollEl && (scrollEl.scrollTop = scrollEl.scrollHeight));
  });
  // Focus the composer on session switch and when the agent asks a free-text question.
  $effect(() => {
    void $session.id;
    c.focus();
  });
  $effect(() => {
    if (sc.loading) return;
    const last = sc.items.at(-1);
    if (last && last.kind === "ask" && last.options.length === 0) c.focus();
  });

  $effect(() => {
    ui.chatComposer = c;
    return () => (ui.chatComposer = null);
  });

  function attach() {
    const inp = document.createElement("input");
    inp.type = "file";
    inp.multiple = true;
    inp.onchange = () => {
      if (!inp.files) return;
      const t = ui.defaultTarget();
      c.attach([...inp.files], t, () => ui.docsChanged());
    };
    inp.click();
  }

  const ctxPct = $derived(sc.ctx && sc.ctx.max ? Math.min(100, (sc.ctx.tokens * 100) / sc.ctx.max) : 0);
  function fmtk(n: number): string {
    return n >= 1000 ? `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k` : String(n);
  }
  function toolSummary(args: string): string {
    const one = args.replace(/\s+/g, " ").trim();
    return one.length > 90 ? one.slice(0, 90) + "…" : one;
  }
</script>

<TopBar>
  {#snippet title()}
    {#if editingTitle}
      <input class="title-input" bind:value={titleDraft} use:focusSelect onblur={commitRename}
        onkeydown={(e) => { if (e.key === "Enter") commitRename(); if (e.key === "Escape") editingTitle = false; }} />
    {:else}
      <button class="title-btn" onclick={startRename} disabled={!!$activeProject}
        title={$activeProject ? "Project session" : "Rename session"}>
        <span class="page-title">{$session.title || "New session"}</span>
      </button>
    {/if}
    <div class="hat-wrap">
      <button class="hat-chip" class:worn={!!$wornHat} onclick={openHats} title="Hat worn by this session">
        <Icon name="hat" size={14} /> {$wornHat || "No hat"} <Icon name="chevronDown" size={12} />
      </button>
      {#if hatMenu}
        <div class="menu-click-away" role="presentation" onclick={() => (hatMenu = false)}></div>
        <div class="popover hat-pop" role="menu">
          <div class="menu-label">Wear a hat</div>
          {#each hats as h (h.name)}
            <button class="menu-item" onclick={() => wear(h.name)} title={h.description}>
              <span class="grow">{h.name}<span class="faint small"> · {h.description}</span></span>
              {#if $wornHat === h.name}<span class="check"><Icon name="check" /></span>{/if}
            </button>
          {/each}
          {#if hats.length === 0}<div class="faint small" style="padding:6px 8px;">No hats defined.</div>{/if}
          <div class="menu-sep"></div>
          <button class="menu-item" onclick={() => wear("")} disabled={!$wornHat}><Icon name="x" /><span class="grow">Take off</span></button>
          <button class="menu-item" onclick={() => { hatMenu = false; ui.openSettings("hats"); }}><Icon name="settings" /><span class="grow">Manage hats…</span></button>
        </div>
      {/if}
    </div>
  {/snippet}
  {#snippet actions()}
    <button class="icon-btn" onclick={() => sc.exportTranscript()} title="Export transcript (Markdown)" aria-label="Export"><Icon name="download" /></button>
    <button class="icon-btn" onclick={clearContext} title="Clear session" aria-label="Clear session"><Icon name="eraser" /></button>
  {/snippet}
</TopBar>

<div class="chat">
  <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events, a11y_mouse_events_have_key_events -->
  <div class="transcript" bind:this={scrollEl} onscroll={onScroll}
    onclick={(e) => { onCiteClick(e); onDocrefClick(e); onCodeCopy(e); }} onmouseover={onCiteHover}>
    <div class="column">
      {#each sc.items as it, i}
        {#if it.kind === "msg"}
          {#if it.role === "assistant"}
            <div class="msg assistant">
              <div class="md">{@html renderMarkdown(it.content)}</div>
              <div class="msg-actions">
                <button class="icon-btn" onclick={() => copyMsg(i, it.content)} title="Copy as Markdown" aria-label="Copy message">
                  <Icon name={copiedIdx === i ? "check" : "copy"} size={14} />
                </button>
                {#if sc.rewindTarget(i)}
                  <button class="icon-btn" onclick={() => rewindTo(i, false)} disabled={sc.loading}
                    title="Rewind to here: remove everything after this reply" aria-label="Rewind to here">
                    <Icon name="rewind" size={14} />
                  </button>
                {/if}
              </div>
            </div>
          {:else}
            <div class="msg user">
              <div class="bubble">{it.content}</div>
              <div class="msg-actions">
                <button class="icon-btn" onclick={() => rewindTo(i, true)} disabled={sc.loading}
                  title="Rewind to here: remove this message and everything after it, and put it back in the composer to edit" aria-label="Rewind to before this message">
                  <Icon name="rewind" size={14} />
                </button>
              </div>
            </div>
          {/if}
        {:else if it.kind === "thinking"}
          <details class="fold">
            <summary><Icon name="brain" size={13} /> Thinking</summary>
            <div class="fold-body wrap">{it.text}</div>
          </details>
        {:else if it.kind === "tool"}
          <details class="fold tool">
            <summary>
              {#if it.done}<Icon name="tool" size={13} />{:else}<span class="spin"><Icon name="clock" size={13} /></span>{/if}
              <span class="tool-name">{it.name}</span>
              {#if it.args}<span class="tool-args">{toolSummary(it.args)}</span>{/if}
              {#if it.done && it.ms}<span class="faint">{it.ms} ms</span>{:else if !it.done}<span class="faint">running…</span>{/if}
            </summary>
            {#if it.args}<pre class="fold-pre">{it.args}</pre>{/if}
            {#if it.done}<pre class="fold-pre out">{it.output || "(no output)"}</pre>{/if}
          </details>
        {:else if it.kind === "stats"}
          <div class="stats">{it.text}</div>
        {:else if it.kind === "error"}
          <div class="turn-error">⚠ {it.text}</div>
        {:else if it.kind === "ask"}
          <div class="ask">
            <div class="ask-q">{it.question}</div>
            {#if it.options.length}
              <div class="row" style="flex-wrap:wrap; gap:6px;">
                {#each it.options as o}<button onclick={() => c.answer(o)}>{o}</button>{/each}
              </div>
            {:else}
              <div class="faint small">Type your answer below.</div>
            {/if}
          </div>
        {/if}
      {/each}
      {#if sc.loading && sc.ppLabel}<div class="faint small">{sc.ppLabel}</div>{/if}
      {#if sc.loading && !sc.ppLabel && sc.items.at(-1)?.kind !== "msg"}<div class="typing"><span></span><span></span><span></span></div>{/if}

      {#if sc.items.length === 0}
        <div class="empty">
          <img src="/logo.png" alt="" class="empty-logo" />
          <h2>What are we working on?</h2>
          <p class="muted">
            {#if $activeProject}This session is shared with everyone in <b>{$activeProject.name}</b>.
            {:else}A personal session — only you can see it.{/if}
          </p>
          <div class="hints">
            <span><kbd>/</kbd> commands</span>
            <span><kbd>{MOD} K</kbd> search everything</span>
            <span><Icon name="upload" size={13} /> drop files to attach them to your message</span>
          </div>
        </div>
      {/if}
    </div>
  </div>

  <div class="composer-wrap">
    <div class="column">
      {#if sc.queue.length}
        <div class="queue">
          <div class="faint small">Queued ({sc.queue.length}) — sent in order as the agent frees up</div>
          {#each sc.queue as q, i}
            <div class="row queued">
              <span class="small wrap" style="flex:1; min-width:0;">{i + 1}. {q}</span>
              <button class="icon-btn" onclick={() => sc.removeQueued(i)} aria-label="Remove"><Icon name="x" size={13} /></button>
            </div>
          {/each}
        </div>
      {/if}
      <div class="composer">
        {#if c.suggestions.length}
          <div class="popover slashmenu" role="listbox">
            {#each c.suggestions as cmd, i}
              <button class="menu-item" class:sel={i === c.slashSel} onmouseenter={() => (c.slashSel = i)} onclick={c.completeSlash}>
                <code>{cmd.name}{#if cmd.args} {cmd.args}{/if}</code><span class="grow muted small">{cmd.desc}</span>
              </button>
            {/each}
          </div>
        {/if}
        <AttachChips {c} />
        <textarea rows="1" placeholder={sc.loading ? "Message (will queue)…" : "Message Harlequin, or / for commands"}
          bind:value={c.input} bind:this={c.el} onkeydown={c.onKey} oninput={c.onInput} disabled={sc.missing}></textarea>
        <div class="composer-bar">
          <button class="icon-btn" onclick={attach} disabled={sc.missing} title="Attach documents to this message (saved to {ui.defaultTarget().label})" aria-label="Attach files"><Icon name="paperclip" /></button>
          {#if sc.missing}<span class="small" style="color: var(--danger);">Session not found here (deleted or moved) — start a new one or pick another.</span>
          {:else if sc.reconnecting}<span class="warn small">Reconnecting…</span>{/if}
          <span class="spacer"></span>
          {#if sc.ctx}
            <button class="ctx" onclick={() => ui.openInspector("session")}
              title="Context: {sc.ctx.tokens.toLocaleString()}{sc.ctx.max ? ' / ' + sc.ctx.max.toLocaleString() : ''} tokens · {sc.ctx.model}">
              {#if sc.ctx.max}
                <svg width="16" height="16" viewBox="0 0 36 36" aria-hidden="true">
                  <circle cx="18" cy="18" r="15" fill="none" stroke="var(--border)" stroke-width="4" />
                  <circle cx="18" cy="18" r="15" fill="none" stroke={ctxPct > 85 ? "var(--danger)" : ctxPct > 65 ? "var(--warn)" : "var(--accent-dim)"}
                    stroke-width="4" stroke-dasharray="{(ctxPct * 94.25) / 100} 94.25" transform="rotate(-90 18 18)" stroke-linecap="round" />
                </svg>
              {/if}
              <span>{fmtk(sc.ctx.tokens)}{sc.ctx.max ? ` / ${fmtk(sc.ctx.max)}` : ""}</span>
              <span class="faint">{sc.ctx.model.split("/").pop()}</span>
            </button>
          {/if}
          {#if sc.loading}
            <button class="send stop" onclick={() => sc.stop()} title="Stop" aria-label="Stop"><Icon name="stop" /></button>
          {:else}
            <button class="send" onclick={() => c.submit()} aria-label="Send"
              disabled={sc.missing || c.uploading || !c.input.trim() || (!$session.id && !c.input.trim().startsWith("/"))}><Icon name="arrowUp" /></button>
          {/if}
        </div>
      </div>
      <div class="foot faint">Enter to send · Shift+Enter for a new line · ↑ recalls earlier messages</div>
    </div>
  </div>
</div>

{#if c.showHelp}
  <div class="modal-scrim" role="presentation" onclick={() => (c.showHelp = false)}></div>
  <div class="modal" role="dialog" aria-label="Commands">
    <header><strong>Commands</strong><span class="spacer"></span>
      <button class="icon-btn" onclick={() => (c.showHelp = false)} aria-label="Close"><Icon name="x" /></button></header>
    <div class="modal-body">
      {#each availableCommands(admin) as cmd}
        <div class="row" style="align-items:baseline;"><code>{cmd.name}{#if cmd.args} {cmd.args}{/if}</code><span class="muted small">{cmd.desc}</span></div>
      {/each}
      <div class="faint small">Everything else is in the sidebar and the command palette ({MOD}+K).</div>
    </div>
  </div>
{/if}

<style>
  .chat { flex: 1; min-height: 0; display: flex; flex-direction: column; }
  .column { width: 100%; max-width: 780px; margin: 0 auto; padding: 0 24px; }
  .transcript { flex: 1; min-height: 0; overflow-y: auto; padding: 20px 0 12px; }
  .transcript .column { display: flex; flex-direction: column; gap: 14px; }

  .title-btn { background: none; border: none; box-shadow: none; padding: 3px 6px; min-height: 0; min-width: 0; }
  .title-btn:hover:not(:disabled) { background: var(--hover); }
  .title-btn:disabled { opacity: 1; cursor: default; }
  .title-input { width: min(380px, 40vw); padding: 3px 8px; font-weight: 600; }
  .hat-wrap { position: relative; }
  .hat-chip { border-radius: 999px; min-height: 26px; padding: 1px 10px; font-size: 12.5px; color: var(--muted); box-shadow: none; gap: 5px; }
  .hat-chip.worn { color: var(--accent-text); border-color: var(--accent-dim); background: var(--accent-soft); }
  .hat-pop { top: calc(100% + 4px); left: 0; width: 320px; }

  .msg.user { display: flex; flex-direction: column; align-items: flex-end; }
  .bubble { background: var(--surface-3); border-radius: 16px 16px 4px 16px; padding: 8px 14px; max-width: 80%;
    white-space: pre-wrap; word-break: break-word; }
  .msg.assistant { position: relative; }
  .msg-actions { display: flex; gap: 2px; opacity: 0; transition: opacity 0.1s; margin-top: 2px; }
  .msg.assistant:hover .msg-actions, .msg.assistant:focus-within .msg-actions,
  .msg.user:hover .msg-actions, .msg.user:focus-within .msg-actions { opacity: 1; }
  .fold { border: 1px solid var(--border-soft); border-radius: var(--radius-sm); background: var(--surface); font-size: 12.5px; }
  .fold summary { cursor: pointer; padding: 5px 10px; color: var(--muted); display: flex; align-items: center; gap: 7px;
    list-style: none; min-width: 0; }
  .fold summary::-webkit-details-marker { display: none; }
  .fold[open] summary { border-bottom: 1px solid var(--border-soft); }
  .fold-body { padding: 8px 10px; color: var(--muted); }
  .tool-name { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--text); flex: none; }
  .tool-args { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--faint); }
  .fold-pre { margin: 8px 10px; max-height: 18em; overflow: auto; white-space: pre-wrap; word-break: break-word; font-size: 12px; }
  .fold-pre.out { color: var(--muted); }
  .turn-error { color: var(--danger); border: 1px solid color-mix(in srgb, var(--danger) 40%, transparent);
    background: var(--danger-soft); border-radius: var(--radius-sm); padding: 7px 12px; }
  .ask { border: 1px solid var(--accent-dim); background: var(--accent-soft); border-radius: var(--radius); padding: 12px 14px;
    display: flex; flex-direction: column; gap: 8px; }
  .typing { display: flex; gap: 4px; padding: 6px 0; }
  .typing span { width: 6px; height: 6px; border-radius: 50%; background: var(--faint); animation: blink 1.2s infinite; }
  .typing span:nth-child(2) { animation-delay: 0.2s; }
  .typing span:nth-child(3) { animation-delay: 0.4s; }
  @keyframes blink { 0%, 80%, 100% { opacity: 0.25; } 40% { opacity: 1; } }

  .empty { text-align: center; margin-top: 14vh; display: flex; flex-direction: column; align-items: center; gap: 4px; }
  .empty h2 { margin: 8px 0 0; font-size: 22px; font-weight: 650; }
  .empty-logo { width: 84px; opacity: 0.9; }
  .hints { display: flex; gap: 18px; flex-wrap: wrap; justify-content: center; margin-top: 14px; color: var(--faint); font-size: 12.5px; }
  .hints span { display: inline-flex; align-items: center; gap: 5px; }

  .composer-wrap { flex: none; padding: 4px 0 10px; }
  .queue { display: flex; flex-direction: column; gap: 2px; padding: 0 4px 6px; }
  .composer { position: relative; background: var(--surface); border: 1px solid var(--border); border-radius: 14px;
    box-shadow: var(--shadow-2); padding: 8px 8px 6px 12px; }
  .composer:focus-within { border-color: var(--accent-dim); }
  .composer textarea { border: none; box-shadow: none; resize: none; padding: 4px 0; max-height: 40vh; background: transparent; }
  .composer textarea:focus { box-shadow: none; }
  .composer-bar { display: flex; align-items: center; gap: 6px; }
  .send { width: 32px; height: 32px; min-height: 0; padding: 0; border-radius: 50%; background: var(--accent-dim);
    border-color: var(--accent-dim); color: var(--on-accent); }
  .send:disabled { background: var(--surface-3); border-color: var(--border); color: var(--faint); }
  .send.stop { background: var(--text); border-color: var(--text); color: var(--bg); }
  .ctx { background: none; border: none; box-shadow: none; min-height: 0; padding: 3px 8px; font-size: 12px; color: var(--muted); gap: 6px; }
  .ctx:hover { background: var(--hover); }
  .slashmenu { left: 0; right: 0; bottom: calc(100% + 6px); }
  .slashmenu code { flex: none; }
  .foot { font-size: 11px; text-align: center; padding-top: 5px; }
</style>
