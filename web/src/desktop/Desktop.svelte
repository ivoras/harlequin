<script lang="ts">
  import { api, getToken, setToken } from "../lib/api";
  import { user, view, session, wornHat, toasts, activeProject, projectSheet, docsVersion, sessionsVersion } from "../lib/stores";
  import { sc } from "../lib/session.svelte";
  import { pc } from "../lib/projectchat.svelte";
  import { docView } from "../lib/docview.svelte";
  import { initSession, ensureSession, setURLParams } from "../lib/workspace";
  import Login from "../views/Login.svelte";
  import Memory from "../views/Memory.svelte";
  import Skills from "../views/Skills.svelte";
  import Cron from "../views/Cron.svelte";
  import Icon from "./Icon.svelte";
  import Sidebar from "./Sidebar.svelte";
  import TopBar from "./TopBar.svelte";
  import ChatView from "./ChatView.svelte";
  import FilesView from "./FilesView.svelte";
  import ProjectsView from "./ProjectsView.svelte";
  import Inspector from "./Inspector.svelte";
  import Palette from "./Palette.svelte";
  import Settings from "./Settings.svelte";
  import { ui, persist } from "./state.svelte";

  let ready = $state(false);
  let started = false;

  // Restore the signed-in user from a stored token.
  $effect(() => {
    if (ready) return;
    (async () => {
      if (getToken()) {
        try {
          user.set(await api.me());
        } catch {
          setToken("");
        }
      }
      ready = true;
    })();
  });

  // Boot / teardown on auth changes (same flow as the mobile UI).
  $effect(() => {
    const u = $user;
    if (u && !started) {
      started = true;
      initSession();
      ui.loadProjects();
    } else if (!u && started) {
      started = false;
      sc.detach();
      session.set({ id: 0, title: "" });
      wornHat.set("");
      activeProject.set(null);
      setURLParams(0, 0);
      view.set("chat");
      ui.sessions = [];
      ui.docs = [];
    }
  });

  // Live session at app scope; ?s=&p= mirror it so refresh resumes.
  $effect(() => {
    const id = $session.id;
    const pid = $activeProject?.id ?? 0;
    if (id) {
      setURLParams(id, pid);
      sc.attach(id, pid);
    }
  });

  // Workspace lists follow the active project; the session list also
  // refreshes when the session changes (a new one was just created).
  let lastPid = -1;
  $effect(() => {
    const pid = $activeProject?.id ?? 0;
    if (!$user) return;
    void $session.id;
    ui.loadSessions();
    if (pid !== lastPid) {
      ui.loadDocs();
      // Entering a project opens its team chat, like the mobile side pane.
      if (pid && lastPid !== -1) ui.openInspector("team");
      lastPid = pid;
    }
  });
  // Server-pushed document changes (agent save/edit/delete, other clients, other
  // project members) refresh the lists; the first run is the initial value.
  let seenDocsVersion = $docsVersion;
  $effect(() => {
    const v = $docsVersion;
    if (v === seenDocsVersion) return;
    seenDocsVersion = v;
    if ($user) ui.docsChanged();
  });
  // The auto-titler also pushes a re-list signal (covers project sessions, which
  // get no session-title notification).
  let seenSessionsVersion = $sessionsVersion;
  $effect(() => {
    const v = $sessionsVersion;
    if (v === seenSessionsVersion) return;
    seenSessionsVersion = v;
    if ($user) ui.loadSessions();
  });
  // Auto-titler updates arrive as session-title notifications → patch the row.
  $effect(() => {
    const { id, title } = $session;
    const row = ui.sessions.find((c) => c.id === id);
    if (row && title && row.title !== title) row.title = title;
  });

  // Team chatroom connection follows the active project.
  $effect(() => {
    const p = $activeProject;
    if (p) pc.open(p.id);
    else pc.close();
  });

  // /project (slash command) opens the Projects page on desktop.
  $effect(() => {
    if ($projectSheet) {
      projectSheet.set(false);
      view.set("projects");
    }
  });

  // Opening a document (citation, file row, docref) shows it in the side panel.
  $effect(() => {
    if (docView.current) ui.openInspector("document");
  });

  // Persist layout choices.
  $effect(() => persist("harlequin.d.sidebar", ui.sidebarOpen));
  $effect(() => persist("harlequin.d.inspector", ui.inspectorOpen));
  $effect(() => persist("harlequin.d.inspectorTab", ui.inspectorTab));
  $effect(() => persist("harlequin.d.inspectorWidth", ui.inspectorWidth));

  // --- keyboard shortcuts ---
  function onKey(e: KeyboardEvent) {
    if (!$user) return;
    const mod = e.ctrlKey || e.metaKey;
    if (!mod) return;
    const k = e.key.toLowerCase();
    if (k === "k" && !e.shiftKey) {
      e.preventDefault();
      ui.paletteOpen = !ui.paletteOpen;
    } else if (k === "o" && e.shiftKey) {
      e.preventDefault();
      ensureSession().then(() => view.set("chat"));
    } else if (k === "b" && !e.shiftKey) {
      e.preventDefault();
      ui.sidebarOpen = !ui.sidebarOpen;
    } else if (k === "." && !e.shiftKey) {
      e.preventDefault();
      ui.inspectorOpen = !ui.inspectorOpen;
    } else if (k === "," && !e.shiftKey) {
      e.preventDefault();
      ui.openSettings();
    }
  }

  // --- drag-and-drop upload (anywhere in the window) ---
  let dragDepth = $state(0);
  const hasFiles = (e: DragEvent) => !!e.dataTransfer && [...e.dataTransfer.types].includes("Files");
  function onDragEnter(e: DragEvent) {
    if (!$user || !hasFiles(e)) return;
    e.preventDefault();
    dragDepth++;
  }
  function onDragOver(e: DragEvent) {
    if (!$user || !hasFiles(e)) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = "copy";
  }
  function onDragLeave(e: DragEvent) {
    if (!hasFiles(e)) return;
    dragDepth = Math.max(0, dragDepth - 1);
  }
  function onDrop(e: DragEvent) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth = 0;
    const files = [...(e.dataTransfer?.files ?? [])];
    if (!files.length) return;
    // On the chat (and no view claiming the drop) files attach to the message.
    if ($view === "chat" && ui.chatComposer && !ui.dropTarget) {
      ui.chatComposer.attach(files, ui.defaultTarget(), () => ui.docsChanged());
    } else ui.upload(files);
  }
  const attaching = $derived(dragDepth > 0 && $view === "chat" && !!ui.chatComposer && !ui.dropTarget);
  const dropLabel = $derived(dragDepth > 0 ? (ui.dropTarget ?? ui.defaultTarget()).label : "");

  // --- inspector resize ---
  let dragging = $state(false);
  function startResize(e: PointerEvent) {
    dragging = true;
    const startX = e.clientX;
    const startW = ui.inspectorWidth;
    const move = (ev: PointerEvent) => {
      ui.inspectorWidth = Math.round(Math.min(Math.max(startW + (startX - ev.clientX), 300), window.innerWidth * 0.6));
    };
    const up = () => {
      dragging = false;
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  }
</script>

<svelte:window onkeydown={onKey} ondragenter={onDragEnter} ondragover={onDragOver} ondragleave={onDragLeave} ondrop={onDrop} />

{#if !ready}
  <div class="boot faint">Loading…</div>
{:else if !$user}
  <Login />
{:else}
  <div class="shell" class:resizing={dragging}>
    {#if ui.sidebarOpen}<Sidebar />{/if}

    <div class="main">
      <!-- Persistent alert box: server-pushed notifications, kept until dismissed. -->
      {#if sc.alerts.length}
        <div class="alerts">
          {#each sc.alerts as a (a.id)}
            <div class="alert">
              <Icon name="bell" />
              <div class="atext">
                <strong>{a.title}</strong>
                {#if a.description}<span class="small muted">{a.description}</span>{/if}
              </div>
              {#if a.prompt}
                <button class="small" onclick={() => { view.set("chat"); sc.runAlert(a); }}><Icon name="play" size={12} /> Run</button>
              {/if}
              <button class="icon-btn" onclick={() => sc.dismissAlert(a)} aria-label="Dismiss"><Icon name="x" size={14} /></button>
            </div>
          {/each}
        </div>
      {/if}

      <div class="main-body">
        {#if $view === "files"}
          <FilesView />
        {:else if $view === "projects"}
          <ProjectsView />
        {:else if $view === "memory"}
          <TopBar label="Memory" /><Memory />
        {:else if $view === "skills"}
          <TopBar label="Skills" /><Skills />
        {:else if $view === "cron"}
          <TopBar label="Scheduled jobs" /><Cron />
        {:else}
          <ChatView />
        {/if}
      </div>
    </div>

    {#if ui.inspectorOpen}
      <div class="resizer" class:dragging role="separator" aria-orientation="vertical" onpointerdown={startResize}></div>
      <Inspector />
    {/if}
  </div>

  <Palette />
  <Settings />

  {#if dropLabel}
    <div class="dropover">
      <div class="dropcard">
        <Icon name="upload" size={32} />
        <div><b>{attaching ? "Drop to attach" : "Drop to add"}</b></div>
        <div class="muted small">{attaching ? "to attach to your message (saved to " + dropLabel + ")" : "to " + dropLabel}</div>
      </div>
    </div>
  {/if}

  {#if ui.uploads.length}
    <div class="uploads">
      {#each ui.uploads as u (u.id)}
        <div class="upl {u.state}">
          {#if u.state === "running"}<span class="spin"><Icon name="clock" size={14} /></span>
          {:else if u.state === "done"}<Icon name="check" size={14} />
          {:else if u.state === "error"}<Icon name="x" size={14} />
          {:else}<Icon name="upload" size={14} />{/if}
          <div class="col" style="gap:0; min-width:0; flex:1;">
            <span class="ellipsize">{u.name}</span>
            <span class="small {u.state === 'error' ? '' : 'faint'} wrap">{u.state === "error" ? u.error : `${u.label} · ${u.target}`}</span>
          </div>
          {#if u.state !== "running"}
            <button class="icon-btn" onclick={() => ui.dismissUpload(u.id)} aria-label="Dismiss"><Icon name="x" size={13} /></button>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
{/if}

<div class="toasts">
  {#each $toasts as t (t.id)}
    <div class="toast {t.kind}">{t.text}</div>
  {/each}
</div>

<style>
  .boot { margin: 30vh auto; }
  .shell.resizing { cursor: col-resize; user-select: none; }
  .dropover { position: fixed; inset: 0; z-index: 70; background: color-mix(in srgb, var(--accent-dim) 14%, var(--scrim));
    display: grid; place-items: center; pointer-events: none; }
  .dropcard { background: var(--surface); border: 2px dashed var(--accent-dim); border-radius: 16px; padding: 28px 48px;
    display: flex; flex-direction: column; align-items: center; gap: 4px; box-shadow: var(--shadow-3); color: var(--accent-text); }
  .uploads { position: fixed; left: 16px; bottom: 16px; z-index: 55; width: 340px; display: flex; flex-direction: column; gap: 6px; }
  .upl { display: flex; align-items: center; gap: 10px; background: var(--surface); border: 1px solid var(--border);
    border-radius: var(--radius-sm); padding: 8px 8px 8px 12px; box-shadow: var(--shadow-2); font-size: 13px; }
  .upl.done { color: var(--accent-text); }
  .upl.error { border-color: var(--danger); color: var(--danger); }
</style>
