<script lang="ts">
  import { api, getToken, setToken } from "./lib/api";
  import { user, view, session, wornHat, toasts, toast, activeProject, projectSheet, type View } from "./lib/stores";
  import { sc } from "./lib/session.svelte";
  import { switchToProject as libSwitchToProject, leaveActiveProject, cleanTitle } from "./lib/project";
  import { pc } from "./lib/projectchat.svelte";
  import type { Session, Project, ProjectInvite } from "./lib/types";
  import Login from "./views/Login.svelte";
  import Chat from "./views/Chat.svelte";
  import Skills from "./views/Skills.svelte";
  import Hats from "./views/Hats.svelte";
  import Memory from "./views/Memory.svelte";
  import Documents from "./views/Documents.svelte";
  import Projects from "./views/Projects.svelte";
  import Mcp from "./views/Mcp.svelte";
  import Cron from "./views/Cron.svelte";
  import Config from "./views/Config.svelte";
  import Usage from "./views/Usage.svelte";
  import Context from "./views/Context.svelte";
  // Tab / menu icons: vendored OpenMoji color SVGs (CC BY-SA 4.0, see
  // src/assets/openmoji/LICENSE.txt) so they render identically everywhere.
  import icChat from "./assets/openmoji/1F4AC.svg";
  import icSkills from "./assets/openmoji/2728.svg";
  import icHats from "./assets/openmoji/1F3AD.svg";
  import icMemory from "./assets/openmoji/1F9E0.svg";
  import icProjects from "./assets/openmoji/1F465.svg";
  import icDocs from "./assets/openmoji/1F4C4.svg";
  import icCron from "./assets/openmoji/23F0.svg";
  import icMcp from "./assets/openmoji/1F50C.svg";
  import icConfig from "./assets/openmoji/2699.svg";
  import icUsage from "./assets/openmoji/1F4CA.svg";

  let ready = $state(false);
  let sessionDrawer = $state(false);
  let menu = $state(false);
  let sessions = $state<Session[]>([]);
  let sessionFilter = $state("");
  // Project management (the /project sheet) + chatroom pane.
  let projects = $state<Project[]>([]);
  let invites = $state<ProjectInvite[]>([]);
  let newProjectName = $state("");
  let inviteEmail = $state("");
  let chatInput = $state("");
  let showChatPane = $state(true);

  let started = false;
  let creating = false;

  // Restore session from a stored token.
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

  // Boot/teardown when auth changes. Notifications are pushed by the server over
  // the session WebSocket (handled in Chat) — no client-side polling.
  $effect(() => {
    const u = $user;
    if (u && !started) {
      started = true;
      initSession();
      loadProjects();
    } else if (!u && started) {
      started = false;
      sc.detach();
      session.set({ id: 0, title: "" });
      wornHat.set("");
      activeProject.set(null);
      setURLParams(0, 0);
      view.set("chat");
    }
  });

  // Connect the live session at app scope (survives view switches) and keep the
  // session id AND active project id in the URL (?s=&p=) so a refresh resumes the
  // same session in the same project. A session inside the active project
  // attaches under that project (shared live session).
  $effect(() => {
    const id = $session.id;
    const pid = $activeProject?.id ?? 0;
    if (id) {
      setURLParams(id, pid);
      sc.attach(id, pid);
    }
  });

  // Open/close the chatroom pane connection as the active project changes.
  $effect(() => {
    const p = $activeProject;
    if (p) pc.open(p.id);
    else pc.close();
  });

  // Load projects + invites whenever the management sheet opens.
  $effect(() => {
    if ($projectSheet) loadProjects();
  });

  function intParam(name: string): number {
    const v = new URLSearchParams(location.search).get(name);
    const n = v ? parseInt(v, 10) : 0;
    return Number.isFinite(n) && n > 0 ? n : 0;
  }
  function setURLParams(sessionID: number, projectID: number): void {
    const url = new URL(location.href);
    if (sessionID) url.searchParams.set("s", String(sessionID));
    else url.searchParams.delete("s");
    if (projectID) url.searchParams.set("p", String(projectID));
    else url.searchParams.delete("p");
    history.replaceState(null, "", url);
  }

  // On boot, resume the project (?p=) and session (?s=) named in the URL, if
  // present; otherwise start a fresh personal session. A project session lives
  // in that project's own session list, not the personal one, so the project
  // must be restored FIRST — otherwise the session lookup misses it entirely
  // and the app silently falls back to an unrelated (or brand new) session.
  async function initSession() {
    const pid = intParam("p");
    const id = intParam("s");
    if (pid) {
      try {
        const p = await api.getProject(pid);
        activeProject.set(p);
        if (id) {
          const list = await api.listProjectSessions(pid);
          const found = list.find((c) => c.id === id);
          if (found) {
            session.set({ id, title: cleanTitle(found.title) });
            wornHat.set(found.hat ?? "");
            return;
          }
          // The session named in the URL no longer belongs to this project
          // (deleted, reassigned) — fall through to opening a project session.
        }
        await libSwitchToProject(p);
        return;
      } catch {
        // Project gone or no longer a member: drop it and fall through to the
        // plain personal-session path below rather than getting stuck.
        activeProject.set(null);
      }
    }
    if (id) {
      try {
        const list = await api.listSessions();
        const found = list.find((c) => c.id === id);
        session.set({ id, title: found ? cleanTitle(found.title) : "" });
        wornHat.set(found?.hat ?? "");
        return;
      } catch {
        /* fall through to a new session */
      }
    }
    await ensureSession();
  }


  async function ensureSession() {
    if (creating) return;
    creating = true;
    try {
      const c = await api.createSession("Session", "");
      session.set({ id: c.id, title: cleanTitle(c.title) });
      wornHat.set("");
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      creating = false;
    }
  }

  // Sessions drawer: type-to-filter (title substring, case-insensitive).
  const filteredSessions = $derived(
    sessionFilter.trim()
      ? sessions.filter((c) => c.title.toLowerCase().includes(sessionFilter.trim().toLowerCase()))
      : sessions,
  );

  // Compact "time since last activity" for the drawer rows ("3m", "2h", "5d").
  function relTime(iso: string): string {
    const secs = (Date.now() - new Date(iso).getTime()) / 1000;
    if (!Number.isFinite(secs) || secs < 60) return "now";
    if (secs < 3600) return Math.floor(secs / 60) + "m";
    if (secs < 86400) return Math.floor(secs / 3600) + "h";
    return Math.floor(secs / 86400) + "d";
  }

  // Focus the drawer's filter input on desktop only — autofocusing on mobile
  // pops the on-screen keyboard over the list.
  function focusOnDesktop(node: HTMLInputElement) {
    if (matchMedia("(min-width: 641px)").matches) node.focus();
  }

  async function openSessions() {
    sessionDrawer = true;
    sessionFilter = "";
    try {
      // In a project, the drawer lists the project's (shared) sessions.
      sessions = $activeProject ? await api.listProjectSessions($activeProject.id) : await api.listSessions();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  // --- projects ---
  async function loadProjects() {
    try {
      [projects, invites] = await Promise.all([api.listProjects(), api.listProjectInvites()]);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function createProject() {
    const name = newProjectName.trim();
    if (!name) return;
    try {
      const p = await api.createProject(name);
      newProjectName = "";
      await loadProjects();
      switchToProject(p);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function acceptInvite(inv: ProjectInvite) {
    try {
      await api.acceptInvite(inv.id);
      await loadProjects();
      const p = projects.find((x) => x.id === inv.project_id);
      if (p) switchToProject(p);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function switchToProject(p: Project) {
    projectSheet.set(false);
    sessionDrawer = false;
    await libSwitchToProject(p);
  }
  async function onSelectProject(e: Event) {
    const id = parseInt((e.target as HTMLSelectElement).value, 10);
    if (!id) {
      await leaveActiveProject();
      return;
    }
    const p = projects.find((x) => x.id === id);
    if (p) await switchToProject(p);
  }
  async function leaveProject() {
    await leaveActiveProject();
  }
  async function departProject() {
    const p = $activeProject;
    if (!p) return;
    if (!confirm(`Depart "${p.name}"? This removes your membership.`)) return;
    try {
      await api.departProject(p.id);
      toast("departed " + p.name);
      projects = projects.filter((x) => x.id !== p.id);
      await leaveProject();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function inviteMember() {
    const p = $activeProject;
    const email = inviteEmail.trim();
    if (!p || !email) return;
    try {
      await api.inviteToProject(p.id, email);
      inviteEmail = "";
      toast("invited " + email);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function assignCurrentSession() {
    const p = $activeProject;
    if (!p || !$session.id) return;
    try {
      const { session_id } = await api.assignSession(p.id, $session.id);
      toast("session assigned to " + p.name);
      session.set({ id: session_id, title: $session.title });
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function takeOffHat() {
    if (!$session.id) return;
    try {
      await api.setSessionHat($session.id, "");
      wornHat.set("");
      toast("hat off");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  function sendChat() {
    pc.send(chatInput);
    chatInput = "";
  }
  async function newSession() {
    sessionDrawer = false;
    await ensureSession();
    view.set("chat");
  }
  function switchSession(c: Session) {
    sessionDrawer = false;
    view.set("chat");
    session.set({ id: c.id, title: cleanTitle(c.title) });
    wornHat.set(c.hat ?? "");
  }
  async function renameSession(c: Session, e: Event) {
    e.stopPropagation();
    const title = prompt("Rename session", c.title)?.trim();
    if (!title || title === c.title) return;
    try {
      await api.renameSession(c.id, title);
      sessions = sessions.map((x) => (x.id === c.id ? { ...x, title } : x));
      if (c.id === $session.id) session.set({ id: c.id, title: cleanTitle(title) });
    } catch (err) {
      toast((err as Error).message, "error");
    }
  }
  async function deleteSession(c: Session, e: Event) {
    e.stopPropagation();
    if (!confirm(`Delete session "${c.title || "#" + c.id}"?`)) return;
    try {
      await api.deleteSession(c.id);
      sessions = sessions.filter((x) => x.id !== c.id);
      if (c.id === $session.id) await ensureSession();
    } catch (err) {
      toast((err as Error).message, "error");
    }
  }
  function logout() {
    api.logout().catch(() => {});
    setToken("");
    user.set(null);
    menu = false;
  }

  const nav: { id: View; label: string; ic: string }[] = [
    { id: "chat", label: "Chat", ic: icChat },
    { id: "skills", label: "Skills", ic: icSkills },
    { id: "hats", label: "Hats", ic: icHats },
    { id: "memory", label: "Memory", ic: icMemory },
    { id: "projects", label: "Projects", ic: icProjects },
    { id: "documents", label: "Docs", ic: icDocs },
    { id: "cron", label: "Cron", ic: icCron },
  ];
  const moreViews: { id: View; label: string; ic: string }[] = [
    { id: "mcp", label: "MCP servers", ic: icMcp },
    { id: "config", label: "Config", ic: icConfig },
    { id: "usage", label: "Usage", ic: icUsage },
    { id: "context", label: "Context", ic: icUsage },
  ];
</script>

{#if !ready}
  <div class="container muted">Loading…</div>
{:else if !$user}
  <Login />
{:else}
  <header class="app-header">
    <button class="iconbtn" onclick={openSessions} aria-label="Sessions">☰</button>
    <span class="brand">Harlequin</span>
    <select class="chip projectselect" value={$activeProject?.id ?? 0} onchange={onSelectProject} title="Project">
      <option value={0}>&lt;no project selected&gt;</option>
      {#each projects as p (p.id)}
        <option value={p.id}>{p.name}</option>
      {/each}
    </select>
    <span class="title">{$session.title || "New session"}</span>
    {#if $wornHat}
      <span class="chip hatchip" title="This session wears the {$wornHat} hat">
        🎩 {$wornHat}
        <button class="offbtn" onclick={takeOffHat} aria-label="Take off the hat">✕</button>
      </span>
    {/if}
    {#if $activeProject}
      <button class="iconbtn" onclick={() => (showChatPane = !showChatPane)} aria-label="Toggle chatroom">💬</button>
    {/if}
    <button class="iconbtn" onclick={() => (menu = true)} aria-label="Menu">⋮</button>
  </header>

  <nav class="tabbar">
    {#each nav as p}
      <button class:active={$view === p.id} onclick={() => view.set(p.id)} title={p.label}>
        <img class="ic" src={p.ic} alt="" /><span class="lbl">{p.label}</span>
      </button>
    {/each}
  </nav>

  <!-- Persistent alert box: server-pushed notifications, visible on every view,
       kept until dismissed. Not part of any session/transcript. -->
  {#if sc.alerts.length}
    <div class="alerts">
      {#each sc.alerts as a (a.id)}
        <div class="alert">
          <span class="bell">🔔</span>
          <div class="atext">
            <strong>{a.title}</strong>
            {#if a.description}<span class="small">{a.description}</span>{/if}
          </div>
          {#if a.prompt}
            <button class="small" onclick={() => { view.set("chat"); sc.runAlert(a); }}>Run</button>
          {/if}
          <button class="ghost small" onclick={() => sc.dismissAlert(a)} aria-label="Dismiss">✕</button>
        </div>
      {/each}
    </div>
  {/if}

  <div class="workspace">
    <main class="app-main">
      {#if $view === "chat"}
        <Chat />
      {:else if $view === "skills"}
        <Skills />
      {:else if $view === "hats"}
        <Hats />
      {:else if $view === "memory"}
        <Memory />
      {:else if $view === "projects"}
        <Projects />
      {:else if $view === "documents"}
        <Documents />
      {:else if $view === "mcp"}
        <Mcp />
      {:else if $view === "cron"}
        <Cron />
      {:else if $view === "config"}
        <Config />
      {:else if $view === "usage"}
        <Usage />
      {:else if $view === "context"}
        <Context />
      {/if}
    </main>

    <!-- Project chatroom side-pane: shown only while a project is active. -->
    {#if $activeProject && showChatPane}
      <aside class="chatpane">
        <header class="chatpane-head">💬 {$activeProject.name}</header>
        <div class="chatpane-msgs">
          {#each pc.messages as m (m.id)}
            <div class="chatmsg">
              <span class="muted small">{m.email}</span>
              <div class="wrap">{m.content}</div>
            </div>
          {/each}
          {#if pc.messages.length === 0}<div class="muted small">No messages yet.</div>{/if}
        </div>
        <div class="chatpane-compose row" style="gap:6px;">
          <input placeholder="Message the team…" bind:value={chatInput}
            onkeydown={(e) => e.key === "Enter" && sendChat()} />
          <button class="primary small" onclick={sendChat} disabled={!chatInput.trim()}>Send</button>
        </div>
      </aside>
    {/if}
  </div>

  {#if sessionDrawer}
    <div class="scrim" role="presentation" onclick={() => (sessionDrawer = false)}></div>
    <aside class="sheet left">
      <header>
        <strong>Sessions</strong><span class="spacer"></span>
        <button class="primary small" onclick={newSession}>+ New</button>
      </header>
      <div class="body list">
        <input placeholder="Filter sessions…" bind:value={sessionFilter} use:focusOnDesktop />
        {#each filteredSessions as c}
          <div class="card row" role="button" tabindex="0" onclick={() => switchSession(c)}
            onkeydown={(e) => e.key === "Enter" && switchSession(c)} style="cursor:pointer;">
            <div class="col" style="gap:2px; min-width:0; flex:1;">
              <div style="overflow:hidden; text-overflow:ellipsis; white-space:nowrap;">{c.title}</div>
              <div class="muted small">#{c.id} · {c.interface} · {relTime(c.updated_at)}</div>
            </div>
            {#if !$activeProject}
              <button class="ghost small" onclick={(e) => renameSession(c, e)} aria-label="Rename">✏️</button>
              <button class="ghost danger small" onclick={(e) => deleteSession(c, e)} aria-label="Delete">✕</button>
            {/if}
          </div>
        {/each}
        {#if filteredSessions.length === 0}<div class="muted small">No sessions.</div>{/if}
      </div>
    </aside>
  {/if}

  {#if menu}
    <div class="scrim" role="presentation" onclick={() => (menu = false)}></div>
    <aside class="sheet bottom">
      <header><strong>Menu</strong><span class="spacer"></span>
        <button class="ghost" onclick={() => (menu = false)}>Close</button></header>
      <div class="body list">
        <div class="muted small">Signed in as {$user.email} ({$user.role})</div>
        <div class="row" style="flex-wrap:wrap; gap:8px;">
          {#each moreViews as v}
            <button class="menuitem" onclick={() => { view.set(v.id); menu = false; }}>
              <img class="ic" src={v.ic} alt="" /> {v.label}
            </button>
          {/each}
        </div>
        <button class="danger" onclick={logout}>Log out</button>
      </div>
    </aside>
  {/if}

  {#if $projectSheet}
    <div class="scrim" role="presentation" onclick={() => projectSheet.set(false)}></div>
    <aside class="sheet bottom">
      <header><strong>Projects</strong><span class="spacer"></span>
        <button class="ghost" onclick={() => projectSheet.set(false)}>Close</button></header>
      <div class="body list">
        {#if invites.length}
          <div class="muted small">Invitations</div>
          {#each invites as inv (inv.id)}
            <div class="card row">
              <div class="col" style="flex:1; min-width:0;">
                <div>{inv.project_name}</div>
                <div class="muted small">from {inv.invited_by}</div>
              </div>
              <button class="primary small" onclick={() => acceptInvite(inv)}>Accept</button>
            </div>
          {/each}
        {/if}

        <div class="muted small">Your projects</div>
        {#each projects as p (p.id)}
          <div class="card row" role="button" tabindex="0" onclick={() => switchToProject(p)}
            onkeydown={(e) => e.key === "Enter" && switchToProject(p)} style="cursor:pointer;">
            <div style="flex:1; min-width:0;">📁 {p.name}</div>
            {#if $activeProject?.id === p.id}<span class="muted small">active</span>{/if}
          </div>
        {/each}
        {#if projects.length === 0}<div class="muted small">No projects yet.</div>{/if}

        <div class="row" style="gap:6px;">
          <input placeholder="New project name" bind:value={newProjectName}
            onkeydown={(e) => e.key === "Enter" && createProject()} />
          <button class="primary small" onclick={createProject} disabled={!newProjectName.trim()}>Create</button>
        </div>

        {#if $activeProject}
          <div class="muted small">Active: {$activeProject.name}</div>
          <div class="row" style="gap:6px;">
            <input placeholder="Invite by email" bind:value={inviteEmail}
              onkeydown={(e) => e.key === "Enter" && inviteMember()} />
            <button class="small" onclick={inviteMember} disabled={!inviteEmail.trim()}>Invite</button>
          </div>
          <div class="row" style="flex-wrap:wrap; gap:8px;">
            <button class="small" onclick={assignCurrentSession}>Assign current session</button>
            <button class="ghost small" onclick={leaveProject}>Leave project (back to personal)</button>
            <button class="ghost small" onclick={departProject}>Depart project (remove membership)</button>
          </div>
        {/if}
      </div>
    </aside>
  {/if}

  <div class="toasts">
    {#each $toasts as t (t.id)}
      <div class="toast {t.kind}">{t.text}</div>
    {/each}
  </div>
{/if}
