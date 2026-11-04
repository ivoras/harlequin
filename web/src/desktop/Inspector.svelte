<script lang="ts">
  import { session, activeProject, wornHat } from "../lib/stores";
  import { sc } from "../lib/session.svelte";
  import { pc } from "../lib/projectchat.svelte";
  import { renderMarkdown } from "../lib/markdown";
  import { docView, closeDocView, openDocPage, downloadDoc, onCiteClick, onDocrefClick, onCodeCopy, focusCited } from "../lib/docview.svelte";
  import Context from "../views/Context.svelte";
  import Icon from "./Icon.svelte";
  import { ui, type InspectorTab } from "./state.svelte";

  // The right-hand panel: the project team chat (while a project is active),
  // the document viewer (citations, file clicks), and session details.
  let chatInput = $state("");
  let chatEl: HTMLDivElement | undefined = $state();

  const tabs = $derived(
    [
      $activeProject ? { id: "team" as InspectorTab, label: "Team chat", icon: "users" } : null,
      docView.current ? { id: "document" as InspectorTab, label: "Document", icon: "file" } : null,
      { id: "session" as InspectorTab, label: "Session", icon: "info" },
    ].filter((t) => t !== null),
  );
  // Fall back to an available tab when the current one disappears.
  $effect(() => {
    if (!tabs.some((t) => t.id === ui.inspectorTab)) ui.inspectorTab = tabs[0].id;
  });

  // Keep the team chat scrolled to the newest message.
  $effect(() => {
    pc.messages.length;
    if (ui.inspectorTab === "team") requestAnimationFrame(() => chatEl && (chatEl.scrollTop = chatEl.scrollHeight));
  });

  function send() {
    pc.send(chatInput);
    chatInput = "";
  }
  function closeDoc() {
    closeDocView();
  }
</script>

<aside class="inspector" style="width:{ui.inspectorWidth}px">
  <div class="tabs-bar">
    {#each tabs as t (t.id)}
      <button class="tab" class:active={ui.inspectorTab === t.id} onclick={() => (ui.inspectorTab = t.id)}>
        <Icon name={t.icon} size={14} /> {t.label}
      </button>
    {/each}
    <span class="spacer"></span>
    <button class="icon-btn" onclick={() => (ui.inspectorOpen = false)} aria-label="Close panel"><Icon name="x" /></button>
  </div>

  {#if ui.inspectorTab === "team" && $activeProject}
    <div class="team">
      <div class="team-head faint small">
        <span class="dot" class:on={pc.connected}></span>
        {$activeProject.name} · visible to all members
      </div>
      <div class="team-msgs" bind:this={chatEl}>
        {#each pc.messages as m (m.id)}
          <div class="tmsg">
            <div class="tmeta"><b>{m.email}</b> <span class="faint">{new Date(m.created_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</span></div>
            <div class="wrap">{m.content}</div>
          </div>
        {/each}
        {#if pc.messages.length === 0}<div class="faint small">No messages yet. Say hi to the team.</div>{/if}
      </div>
      <div class="team-compose">
        <input placeholder="Message the team…" bind:value={chatInput} onkeydown={(e) => e.key === "Enter" && send()} />
        <button class="primary small" onclick={send} disabled={!chatInput.trim()}>Send</button>
      </div>
    </div>
  {:else if ui.inspectorTab === "document" && docView.current}
    <div class="doc">
      <div class="doc-head">
        <strong class="ellipsize" title={docView.current.title}>{docView.current.title}</strong>
        <span class="spacer"></span>
        {#if docView.current.ref}
          <button class="icon-btn" title="Open as a page in a new tab" onclick={openDocPage}><Icon name="external" /></button>
        {/if}
        <button class="icon-btn" title="Download" onclick={downloadDoc}><Icon name="download" /></button>
        <button class="icon-btn" title="Close document" onclick={closeDoc}><Icon name="x" /></button>
      </div>
      <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
      <div class="doc-body md" use:focusCited={[docView.current.text, docView.focus]} onclick={(e) => { onCiteClick(e); onDocrefClick(e); onCodeCopy(e); }}>
        {@html renderMarkdown(docView.current.text)}
      </div>
    </div>
  {:else}
    <div class="sess">
      <div class="kvs">
        <div class="kv"><b>Session</b><span>{$session.title || "New session"} <span class="faint">#{$session.id}</span></span></div>
        <div class="kv"><b>Workspace</b><span>{$activeProject ? `Project “${$activeProject.name}” (shared)` : "Personal"}</span></div>
        <div class="kv"><b>Hat</b><span>{$wornHat || "none"}</span></div>
        {#if sc.ctx}<div class="kv"><b>Model</b><span class="ellipsize">{sc.ctx.model}</span></div>{/if}
      </div>
      <div class="row" style="padding: 0 16px; gap:6px;">
        <button class="small" onclick={() => sc.exportTranscript()}><Icon name="download" size={13} /> Export</button>
      </div>
      <!-- Re-mount after every turn so the breakdown reflects the latest context. -->
      {#key `${$session.id}:${sc.ctx?.tokens ?? 0}`}
        <Context />
      {/key}
    </div>
  {/if}
</aside>

<style>
  .inspector { flex: none; min-width: 300px; max-width: 60vw; display: flex; flex-direction: column; min-height: 0;
    background: var(--surface); border-left: 1px solid var(--border-soft); }
  .tabs-bar { display: flex; align-items: center; gap: 2px; padding: 0 8px; height: 48px; border-bottom: 1px solid var(--border-soft); flex: none; }
  .tab { background: none; border: none; box-shadow: none; color: var(--muted); padding: 4px 9px; min-height: 28px; font-size: 13px; gap: 6px; }
  .tab:hover { background: var(--hover); color: var(--text); }
  .tab.active { color: var(--text); background: var(--surface-2); font-weight: 600; }
  .team, .doc, .sess { flex: 1; min-height: 0; display: flex; flex-direction: column; }
  .team-head { padding: 8px 14px; display: flex; align-items: center; gap: 6px; border-bottom: 1px solid var(--border-soft); }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--faint); }
  .dot.on { background: var(--accent-dim); }
  .team-msgs { flex: 1; min-height: 0; overflow-y: auto; padding: 12px 14px; display: flex; flex-direction: column; gap: 12px; }
  .tmeta { font-size: 12px; }
  .team-compose { display: flex; gap: 6px; padding: 10px; border-top: 1px solid var(--border-soft); }
  .doc-head { display: flex; align-items: center; gap: 4px; padding: 8px 10px 8px 16px; border-bottom: 1px solid var(--border-soft); }
  .doc-body { flex: 1; min-height: 0; overflow-y: auto; padding: 16px 20px; }
  .sess { overflow-y: auto; }
  .kvs { display: flex; flex-direction: column; gap: 4px; padding: 14px 16px 10px; font-size: 13px; }
  .kv span { min-width: 0; }
  .sess :global(.panel) { overflow: visible; flex: none; }
  .sess :global(.container) { padding: 10px 16px; }
</style>
