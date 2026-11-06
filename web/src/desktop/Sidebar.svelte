<script lang="ts">
  import { api, setToken } from "../lib/api";
  import { onMount } from "svelte";
  import { user, view, session, activeProject, toast, orgName, loadOrgName, type View } from "../lib/stores";
  import { ensureSession } from "../lib/workspace";
  import { cleanTitle } from "../lib/project";
  import { viewDocument } from "../lib/docview.svelte";
  import { switchUI } from "../lib/uipref";
  import type { Document, Session } from "../lib/types";
  import Icon from "./Icon.svelte";
  import WorkspaceSwitcher from "./WorkspaceSwitcher.svelte";
  import McpTools from "./McpTools.svelte";
  import { ui, openSession, relTime, MOD } from "./state.svelte";
  import { theme, setTheme } from "./theme.svelte";

  let filter = $state("");
  let userMenu = $state(false);
  let filesOpen = $state(true);
  // The "…" menu of a sidebar file row; fixed-positioned from the button's
  // rect because the list scrolls (an absolute popover would be clipped).
  let fileMenu = $state<{ d: Document; x: number; y: number } | null>(null);
  function openFileMenu(d: Document, e: MouseEvent) {
    e.stopPropagation();
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    fileMenu = fileMenu?.d === d ? null : { d, x: r.left, y: r.bottom + 2 };
  }

  const nav: { id: View; label: string; icon: string }[] = [
    { id: "chat", label: "Chat", icon: "message" },
    { id: "files", label: "Files", icon: "file" },
    { id: "memory", label: "Memory", icon: "brain" },
    { id: "skills", label: "Skills", icon: "sparkles" },
    { id: "cron", label: "Scheduled jobs", icon: "clock" },
    { id: "projects", label: "Projects", icon: "users" },
  ];

  // The sidebar's file list is the workspace's own corpus: the project's files
  // in a project, otherwise the user's personal files (shared org documents
  // live on the Files page, under "Shared").
  const wsDocs = $derived(ui.docs.filter((d) => d.scope === ($activeProject ? "project" : "personal")));

  const filtered = $derived.by(() => {
    const f = filter.trim().toLowerCase();
    return f ? ui.sessions.filter((c) => (c.title || "").toLowerCase().includes(f)) : ui.sessions;
  });

  // Group by last activity, newest first, like most chat apps.
  const groups = $derived.by(() => {
    const now = new Date();
    const startToday = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
    const day = 86_400_000;
    const buckets: { label: string; items: Session[] }[] = [
      { label: "Today", items: [] },
      { label: "Yesterday", items: [] },
      { label: "Previous 7 days", items: [] },
      { label: "Previous 30 days", items: [] },
      { label: "Older", items: [] },
    ];
    const sorted = [...filtered].sort((a, b) => b.updated_at.localeCompare(a.updated_at));
    for (const c of sorted) {
      const t = new Date(c.updated_at).getTime();
      const i = t >= startToday ? 0 : t >= startToday - day ? 1 : t >= startToday - 7 * day ? 2 : t >= startToday - 30 * day ? 3 : 4;
      buckets[i].items.push(c);
    }
    return buckets.filter((b) => b.items.length);
  });

  async function newSession() {
    await ensureSession();
    view.set("chat");
  }
  async function rename(c: Session, e: Event) {
    e.stopPropagation();
    const title = prompt("Rename session", c.title)?.trim();
    if (!title || title === c.title) return;
    try {
      await api.renameSession(c.id, title);
      c.title = title;
      if (c.id === $session.id) session.set({ id: c.id, title });
    } catch (err) {
      toast((err as Error).message, "error");
    }
  }
  async function del(c: Session, e: Event) {
    e.stopPropagation();
    if (!confirm(`Delete session "${c.title || "#" + c.id}"?`)) return;
    try {
      await api.deleteSession(c.id);
      ui.sessions = ui.sessions.filter((x) => x.id !== c.id);
      if (c.id === $session.id) await ensureSession();
    } catch (err) {
      toast((err as Error).message, "error");
    }
  }
  function logout() {
    userMenu = false;
    api.logout().catch(() => {});
    setToken("");
    user.set(null);
  }
  function pickFiles() {
    const inp = document.createElement("input");
    inp.type = "file";
    inp.multiple = true;
    inp.onchange = () => inp.files && ui.upload([...inp.files], ui.defaultTarget());
    inp.click();
  }
  onMount(loadOrgName);
</script>

<aside class="sidebar">
  <div class="sb-top">
    <img src="/favicon.png" alt="" class="logo" />
    <span class="brand">Harlequin</span>
    {#if $orgName}<span class="org" title="Organisation">{$orgName}</span>{/if}
    <span class="spacer"></span>
    <button class="icon-btn" onclick={() => (ui.sidebarOpen = false)} title="Hide sidebar ({MOD}+B)" aria-label="Hide sidebar">
      <Icon name="panelLeft" />
    </button>
  </div>

  <div class="sb-actions">
    <button class="sb-new" onclick={newSession} title="New session ({MOD}+Shift+O)">
      <Icon name="plus" /> <span class="grow">New session</span>
    </button>
    <button class="sb-search" onclick={() => (ui.paletteOpen = true)} title="Search and commands">
      <Icon name="search" /> <span class="grow">Search…</span> <kbd>{MOD} K</kbd>
    </button>
  </div>

  <WorkspaceSwitcher />

  <nav class="sb-nav">
    {#each nav as n}
      <button class="nav-item" class:active={$view === n.id} onclick={() => view.set(n.id)}>
        <Icon name={n.icon} /> <span class="grow">{n.label}</span>
        {#if n.id === "projects" && ui.invites.length}<span class="count">{ui.invites.length}</span>{/if}
      </button>
      {#if n.id === "cron"}<McpTools />{/if}
    {/each}
  </nav>

  <section class="sb-section files">
    <div class="sb-head">
      <button class="sb-toggle" onclick={() => (filesOpen = !filesOpen)} aria-expanded={filesOpen}>
        <span class="chev" class:open={filesOpen}><Icon name="chevronRight" size={12} /></span>
        {$activeProject ? "Project files" : "My files"}
        <span class="faint">{wsDocs.length || ""}</span>
      </button>
      <span class="spacer"></span>
      <button class="icon-btn" onclick={pickFiles} title="Upload to {ui.defaultTarget().label}" aria-label="Upload files">
        <Icon name="upload" size={14} />
      </button>
    </div>
    {#if filesOpen}
      <div class="sb-files">
        {#each wsDocs.slice(0, 8) as d (`${d.scope}:${d.id}`)}
          <div class="file-row" class:open={fileMenu?.d === d}>
            <button class="file-item" title={d.description || d.title}
              onclick={() => viewDocument(d, d.scope === "project" ? ($activeProject?.id ?? 0) : 0)}>
              <Icon name="file" size={14} /> <span class="grow">{d.title}</span>
            </button>
            <button class="icon-btn file-more" aria-label="More actions for {d.title}" aria-haspopup="menu"
              aria-expanded={fileMenu?.d === d} onclick={(e) => openFileMenu(d, e)}><Icon name="more" size={14} /></button>
          </div>
        {/each}
        {#if wsDocs.length === 0}
          <div class="sb-empty">Drop files anywhere to add them.</div>
        {:else if wsDocs.length > 8}
          <button class="file-item more" onclick={() => view.set("files")}>Show all {wsDocs.length}…</button>
        {/if}
      </div>
    {/if}
  </section>

  {#if fileMenu}
    <div class="menu-click-away" role="presentation" onclick={() => (fileMenu = null)}></div>
    <div class="popover" role="menu" style="position:fixed; left:{fileMenu.x}px; top:{fileMenu.y}px; min-width:150px;">
      <button class="menu-item del" role="menuitem"
        title="Permanently delete this file, its Markdown text and its search index entries"
        onclick={() => { const d = fileMenu!.d; fileMenu = null; ui.deleteDoc(d, $activeProject?.id ?? 0); }}>
        <Icon name="trash" size={14} /><span class="grow">Delete…</span>
      </button>
    </div>
  {/if}

  <section class="sb-section sessions">
    <div class="sb-head">
      <span class="sb-title">{$activeProject ? "Project sessions" : "Sessions"}</span>
    </div>
    <div class="sb-filter">
      <input placeholder="Filter sessions" bind:value={filter} />
    </div>
    <div class="sb-sessions">
      {#each groups as g (g.label)}
        <div class="group-label">{g.label}</div>
        {#each g.items as c (c.id)}
          <div class="sess" class:active={c.id === $session.id} role="button" tabindex="0"
            onclick={() => openSession(c)} onkeydown={(e) => e.key === "Enter" && openSession(c)}
            title="#{c.id} · {c.interface}{c.owner_email ? ' · ' + c.owner_email : ''}">
            <span class="grow">{cleanTitle(c.title) || "New session"}</span>
            <span class="when faint">{relTime(c.updated_at)}</span>
            {#if !$activeProject}
              <span class="sess-actions">
                <button class="icon-btn" onclick={(e) => rename(c, e)} aria-label="Rename"><Icon name="pencil" size={13} /></button>
                <button class="icon-btn" onclick={(e) => del(c, e)} aria-label="Delete"><Icon name="trash" size={13} /></button>
              </span>
            {/if}
          </div>
        {/each}
      {/each}
      {#if filtered.length === 0}<div class="sb-empty">No sessions{filter ? " match" : " yet"}.</div>{/if}
    </div>
  </section>

  <div class="sb-user">
    <button class="user-btn" onclick={() => (userMenu = !userMenu)} aria-haspopup="menu">
      <span class="avatar">{($user?.email ?? "?").slice(0, 1).toUpperCase()}</span>
      <span class="user-name">
        <span class="ellipsize">{$user?.email}</span>
        <span class="faint small">{$user?.role}</span>
      </span>
      <Icon name="more" />
    </button>
    {#if userMenu}
      <div class="menu-click-away" role="presentation" onclick={() => (userMenu = false)}></div>
      <div class="popover user-pop" role="menu">
        <button class="menu-item" onclick={() => { userMenu = false; ui.openSettings(); }}>
          <Icon name="settings" /><span class="grow">Settings</span><kbd>{MOD} ,</kbd>
        </button>
        <button class="menu-item" onclick={() => { userMenu = false; ui.openSettings("hats"); }}>
          <Icon name="hat" /><span class="grow">Hats</span>
        </button>
        <div class="menu-label">Theme</div>
        <div class="theme-row">
          {#each [["system", "monitor", "System"], ["light", "sun", "Light"], ["dark", "moon", "Dark"]] as [id, ic, label]}
            <button class="small" class:on={theme.pref === id} onclick={() => setTheme(id as "system" | "light" | "dark")}>
              <Icon name={ic} size={13} /> {label}
            </button>
          {/each}
        </div>
        <div class="menu-sep"></div>
        <button class="menu-item" onclick={() => switchUI("mobile")}>
          <Icon name="phone" /><span class="grow">Mobile version</span>
        </button>
        <button class="menu-item danger" onclick={logout}>
          <Icon name="logout" /><span class="grow">Log out</span>
        </button>
      </div>
    {/if}
  </div>
</aside>

<style>
  .sidebar { width: 272px; flex: none; display: flex; flex-direction: column; min-height: 0;
    background: var(--sidebar); border-right: 1px solid var(--border-soft); }
  .sb-top { display: flex; align-items: center; gap: 8px; padding: 10px 10px 6px 14px; height: 48px; }
  .logo { width: 22px; height: 22px; border-radius: 5px; }
  .org { color: var(--text-dim, var(--muted, inherit)); font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .org::before { content: "·"; margin-right: 6px; }
  .brand { font-weight: 750; letter-spacing: 0.03em; color: var(--accent-text); }
  .grow { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; text-align: left; }
  .sb-actions { display: flex; flex-direction: column; gap: 4px; padding: 4px 10px 10px; }
  .sb-new, .sb-search { justify-content: flex-start; width: 100%; gap: 8px; }
  .sb-new { font-weight: 600; }
  .sb-search { background: transparent; box-shadow: none; color: var(--muted); border-color: var(--border-soft); }
  .sb-nav { display: flex; flex-direction: column; gap: 1px; padding: 10px 10px 4px; }
  .nav-item, .file-item { width: 100%; justify-content: flex-start; gap: 9px; background: transparent; border: none;
    box-shadow: none; padding: 5px 8px; min-height: 30px; color: var(--muted); border-radius: 6px; }
  .nav-item:hover, .file-item:hover { background: var(--hover); color: var(--text); }
  .nav-item.active { background: var(--surface); color: var(--text); box-shadow: var(--shadow-1); font-weight: 600; }
  .nav-item.active :global(.icon) { color: var(--accent-text); }
  .count { background: var(--accent-dim); color: var(--on-accent); font-size: 11px; border-radius: 999px; padding: 0 6px; font-weight: 700; }
  .sb-section { display: flex; flex-direction: column; min-height: 0; padding: 6px 10px 0; }
  .sb-section.sessions { flex: 1; }
  .sb-head { display: flex; align-items: center; padding: 4px 4px 2px 6px; }
  .sb-toggle { background: none; border: none; box-shadow: none; padding: 2px; min-height: 0; gap: 5px;
    font-size: 11.5px; font-weight: 600; color: var(--faint); text-transform: uppercase; letter-spacing: 0.04em; }
  .sb-toggle:hover { background: none; color: var(--text); }
  .sb-title { font-size: 11.5px; font-weight: 600; color: var(--faint); text-transform: uppercase; letter-spacing: 0.04em; padding: 2px; }
  .chev { display: inline-flex; transition: transform 0.12s; }
  .chev.open { transform: rotate(90deg); }
  .sb-files { display: flex; flex-direction: column; max-height: 30vh; overflow-y: auto; }
  .file-item { font-size: 13px; min-height: 26px; }
  .file-row { position: relative; }
  .file-more { display: none; position: absolute; right: 2px; top: 50%; transform: translateY(-50%); padding: 3px; }
  .file-row:hover .file-more, .file-row:focus-within .file-more, .file-row.open .file-more { display: inline-flex; }
  .menu-item.del { color: var(--danger); }
  .file-item.more { color: var(--faint); padding-left: 31px; }
  .sb-empty { color: var(--faint); font-size: 12px; padding: 4px 8px 6px; }
  .sb-filter { padding: 4px 0 6px; }
  .sb-filter input { padding: 4px 8px; font-size: 13px; background: var(--bg); }
  .sb-sessions { flex: 1; min-height: 0; overflow-y: auto; padding-bottom: 8px; }
  .group-label { font-size: 11px; color: var(--faint); padding: 8px 8px 2px; font-weight: 600; }
  .sess { display: flex; align-items: center; gap: 6px; padding: 5px 8px; border-radius: 6px; cursor: pointer;
    font-size: 13px; color: var(--text); min-height: 30px; position: relative; }
  .sess:hover { background: var(--hover); }
  .sess.active { background: var(--surface); box-shadow: var(--shadow-1); }
  .sess .when { font-size: 11px; }
  .sess-actions { display: none; gap: 0; }
  .sess:hover .sess-actions { display: inline-flex; }
  .sess:hover .when { display: none; }
  .sess-actions :global(button) { padding: 3px; }
  .sb-user { position: relative; padding: 8px 10px; border-top: 1px solid var(--border-soft); }
  .user-btn { width: 100%; justify-content: flex-start; gap: 9px; background: transparent; border: none; box-shadow: none;
    padding: 5px 6px; text-align: left; }
  .user-btn:hover { background: var(--hover); }
  .avatar { width: 28px; height: 28px; border-radius: 50%; display: grid; place-items: center; flex: none;
    background: var(--accent-dim); color: var(--on-accent); font-weight: 700; font-size: 13px; }
  .user-name { flex: 1; min-width: 0; display: flex; flex-direction: column; line-height: 1.25; font-size: 13px; }
  .user-pop { left: 10px; right: 10px; bottom: calc(100% + 4px); }
  .theme-row { display: flex; gap: 4px; padding: 0 6px 6px; }
  .theme-row button { flex: 1; box-shadow: none; gap: 4px; }
  .theme-row button.on { border-color: var(--accent-dim); color: var(--accent-text); background: var(--accent-soft); }
  .menu-item.danger { color: var(--danger); }
</style>
