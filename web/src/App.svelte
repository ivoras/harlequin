<script lang="ts">
  import { api, getToken, setToken } from "./lib/api";
  import { user, view, session, toasts, toast, type View } from "./lib/stores";
  import { NOTIFY_SESSION_TITLE } from "./lib/types";
  import type { Conversation } from "./lib/types";
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
  let convDrawer = $state(false);
  let menu = $state(false);
  let convos = $state<Conversation[]>([]);

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
      ensureConversation();
      pollNotifications();
      notifTimer = setInterval(pollNotifications, 30000);
    } else if (!u && started) {
      started = false;
      if (notifTimer) clearInterval(notifTimer);
      notifTimer = undefined;
      session.set({ id: 0, title: "" });
      view.set("chat");
    }
  });

  function cleanTitle(t: string): string {
    return t === "Session" || t === "New conversation" ? "" : t;
  }

  async function ensureConversation() {
    if (creating) return;
    creating = true;
    try {
      const c = await api.createConversation("Session", "");
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
          if (n.conversation_id === $session.id) session.update((s) => ({ ...s, title: n.title }));
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

  async function openConvos() {
    convDrawer = true;
    try {
      convos = await api.listConversations();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function newConversation() {
    convDrawer = false;
    await ensureConversation();
    view.set("chat");
  }
  function switchConversation(c: Conversation) {
    convDrawer = false;
    view.set("chat");
    session.set({ id: c.id, title: cleanTitle(c.title) });
  }
  async function deleteConversation(c: Conversation, e: Event) {
    e.stopPropagation();
    try {
      await api.deleteConversation(c.id);
      convos = convos.filter((x) => x.id !== c.id);
      if (c.id === $session.id) await ensureConversation();
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
    <button class="iconbtn" onclick={openConvos} aria-label="Conversations">☰</button>
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

  {#if convDrawer}
    <div class="scrim" role="presentation" onclick={() => (convDrawer = false)}></div>
    <aside class="sheet left">
      <header>
        <strong>Conversations</strong><span class="spacer"></span>
        <button class="primary small" onclick={newConversation}>+ New</button>
      </header>
      <div class="body list">
        {#each convos as c}
          <div class="card row" role="button" tabindex="0" onclick={() => switchConversation(c)}
            onkeydown={(e) => e.key === "Enter" && switchConversation(c)} style="cursor:pointer;">
            <div class="col" style="gap:2px; min-width:0; flex:1;">
              <div style="overflow:hidden; text-overflow:ellipsis; white-space:nowrap;">{c.title}</div>
              <div class="muted small">#{c.id} · {c.interface}</div>
            </div>
            <button class="ghost danger small" onclick={(e) => deleteConversation(c, e)} aria-label="Delete">✕</button>
          </div>
        {/each}
        {#if convos.length === 0}<div class="muted small">No conversations.</div>{/if}
      </div>
    </aside>
  {/if}

  {#if menu}
    <div class="scrim" role="presentation" onclick={() => (menu = false)}></div>
    <aside class="sheet bottom">
      <header><strong>Menu</strong><span class="spacer"></span>
        <button class="ghost" onclick={() => (menu = false)}>Close</button></header>
      <div class="body list">
        <div class="muted small">Signed in as {$user.username} ({$user.role})</div>
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
