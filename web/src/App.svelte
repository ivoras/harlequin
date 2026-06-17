<script lang="ts">
  import { api, getToken, setToken } from "./lib/api";
  import { user, view, session, toasts, toast, type View } from "./lib/stores";
  import { NOTIFY_SESSION_TITLE } from "./lib/types";
  import type { Session } from "./lib/types";
  import Login from "./views/Login.svelte";
  import Chat from "./views/Chat.svelte";
  import Skills from "./views/Skills.svelte";
  import Hats from "./views/Hats.svelte";
  import Memory from "./views/Memory.svelte";
  import Documents from "./views/Documents.svelte";
  import Mcp from "./views/Mcp.svelte";
  import Cron from "./views/Cron.svelte";
  import Config from "./views/Config.svelte";
  import Usage from "./views/Usage.svelte";

  let ready = $state(false);
  let sessionDrawer = $state(false);
  let menu = $state(false);
  let sessions = $state<Session[]>([]);

  let started = false;
  let notifTimer: ReturnType<typeof setInterval> | undefined;
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

  // Boot/teardown when auth changes.
  $effect(() => {
    const u = $user;
    if (u && !started) {
      started = true;
      initSession();
      pollNotifications();
      notifTimer = setInterval(pollNotifications, 30000);
    } else if (!u && started) {
      started = false;
      if (notifTimer) clearInterval(notifTimer);
      notifTimer = undefined;
      session.set({ id: 0, title: "" });
      setSessionParam(0);
      view.set("chat");
    }
  });

  // Keep the session id in the URL (?c=) so a refresh resumes the same session.
  $effect(() => {
    const id = $session.id;
    if (id) setSessionParam(id);
  });

  function sessionParam(): number {
    const v = new URLSearchParams(location.search).get("c");
    const n = v ? parseInt(v, 10) : 0;
    return Number.isFinite(n) && n > 0 ? n : 0;
  }
  function setSessionParam(id: number): void {
    const url = new URL(location.href);
    if (id) url.searchParams.set("c", String(id));
    else url.searchParams.delete("c");
    history.replaceState(null, "", url);
  }

  // On boot, resume the session named in the URL (?c=) if present; otherwise start
  // a fresh one. Resuming reconnects to its live server-side goroutine.
  async function initSession() {
    const id = sessionParam();
    if (id) {
      try {
        const list = await api.listSessions();
        const found = list.find((c) => c.id === id);
        session.set({ id, title: found ? cleanTitle(found.title) : "" });
        return;
      } catch {
        /* fall through to a new session */
      }
    }
    await ensureSession();
  }

  function cleanTitle(t: string): string {
    return t === "Session" || t === "New session" || t === "New conversation" ? "" : t;
  }

  async function ensureSession() {
    if (creating) return;
    creating = true;
    try {
      const c = await api.createSession("Session", "");
      session.set({ id: c.id, title: cleanTitle(c.title) });
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      creating = false;
    }
  }

  async function pollNotifications() {
    try {
      const list = await api.listNotifications();
      for (const n of list) {
        if (n.kind === NOTIFY_SESSION_TITLE) {
          if (n.session_id === $session.id) session.update((s) => ({ ...s, title: n.title }));
          await api.ackNotification(n.id);
        } else if (!n.auto_run) {
          toast(n.description ? `${n.title} — ${n.description}` : n.title);
          await api.ackNotification(n.id);
        }
        // auto_run notifications are left for a client that runs them.
      }
    } catch {
      /* ignore transient poll errors */
    }
  }

  async function openSessions() {
    sessionDrawer = true;
    try {
      sessions = await api.listSessions();
    } catch (e) {
      toast((e as Error).message, "error");
    }
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
  }
  async function deleteSession(c: Session, e: Event) {
    e.stopPropagation();
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
    { id: "chat", label: "Chat", ic: "💬" },
    { id: "skills", label: "Skills", ic: "📚" },
    { id: "hats", label: "Hats", ic: "🎩" },
    { id: "memory", label: "Memory", ic: "🧠" },
    { id: "cron", label: "Cron", ic: "⏰" },
  ];
  const moreViews: View[] = ["documents", "mcp", "config", "usage"];
</script>

{#if !ready}
  <div class="container muted">Loading…</div>
{:else if !$user}
  <Login />
{:else}
  <header class="app-header">
    <button class="iconbtn" onclick={openSessions} aria-label="Sessions">☰</button>
    <span class="brand">Harlequin</span>
    <span class="title">{$session.title || "New session"}</span>
    <button class="iconbtn" onclick={() => (menu = true)} aria-label="Menu">⋮</button>
  </header>

  <nav class="tabbar">
    {#each nav as p}
      <button class:active={$view === p.id} onclick={() => view.set(p.id)}>
        <span class="ic">{p.ic}</span><span>{p.label}</span>
      </button>
    {/each}
  </nav>

  <main class="app-main">
    {#if $view === "chat"}
      <Chat />
    {:else if $view === "skills"}
      <Skills />
    {:else if $view === "hats"}
      <Hats />
    {:else if $view === "memory"}
      <Memory />
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
    {/if}
  </main>

  {#if sessionDrawer}
    <div class="scrim" role="presentation" onclick={() => (sessionDrawer = false)}></div>
    <aside class="sheet left">
      <header>
        <strong>Sessions</strong><span class="spacer"></span>
        <button class="primary small" onclick={newSession}>+ New</button>
      </header>
      <div class="body list">
        {#each sessions as c}
          <div class="card row" role="button" tabindex="0" onclick={() => switchSession(c)}
            onkeydown={(e) => e.key === "Enter" && switchSession(c)} style="cursor:pointer;">
            <div class="col" style="gap:2px; min-width:0; flex:1;">
              <div style="overflow:hidden; text-overflow:ellipsis; white-space:nowrap;">{c.title}</div>
              <div class="muted small">#{c.id} · {c.interface}</div>
            </div>
            <button class="ghost danger small" onclick={(e) => deleteSession(c, e)} aria-label="Delete">✕</button>
          </div>
        {/each}
        {#if sessions.length === 0}<div class="muted small">No sessions.</div>{/if}
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
            <button onclick={() => { view.set(v); menu = false; }}>{v}</button>
          {/each}
        </div>
        <button class="danger" onclick={logout}>Log out</button>
      </div>
    </aside>
  {/if}

  <div class="toasts">
    {#each $toasts as t (t.id)}
      <div class="toast {t.kind}">{t.text}</div>
    {/each}
  </div>
{/if}
