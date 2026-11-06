<script lang="ts">
  import { api, setToken } from "../lib/api";
  import { user, toast } from "../lib/stores";
  import { switchUI } from "../lib/uipref";
  import { isElevated, isOwner } from "../lib/types";
  import Hats from "../views/Hats.svelte";
  import Mcp from "../views/Mcp.svelte";
  import Config from "../views/Config.svelte";
  import Usage from "../views/Usage.svelte";
  import Audit from "../views/Audit.svelte";
  import Users from "../views/Users.svelte";
  import Icon from "./Icon.svelte";
  import { ui, type SettingsTab } from "./state.svelte";
  import { theme, setTheme, type ThemePref } from "./theme.svelte";

  // Account- and admin-level settings in one modal; the management panes are
  // the same components the mobile UI uses.
  const tabs: { id: SettingsTab; label: string; icon: string }[] = $derived([
    { id: "general", label: "General", icon: "settings" },
    { id: "login", label: "Login", icon: "key" },
    // Only owners can manage accounts (the API enforces it too).
    ...(isOwner($user?.role) ? [{ id: "users" as SettingsTab, label: "Users", icon: "users" }] : []),
    { id: "hats", label: "Hats", icon: "hat" },
    { id: "mcp", label: "MCP servers", icon: "plug" },
    { id: "config", label: "Config", icon: "key" },
    { id: "usage", label: "Usage", icon: "chart" },
    ...(isOwner($user?.role) ? [{ id: "audit" as SettingsTab, label: "Audit", icon: "shield" }] : []),
  ]);
  const themes: { id: ThemePref; label: string; icon: string }[] = [
    { id: "system", label: "System", icon: "monitor" },
    { id: "light", label: "Light", icon: "sun" },
    { id: "dark", label: "Dark", icon: "moon" },
  ];
  let alertText = $state("");
  let curPw = $state("");
  let newPw = $state("");
  let newPw2 = $state("");

  async function changePassword() {
    if (newPw.length < 8) return toast("password must be at least 8 characters", "error");
    if (newPw !== newPw2) return toast("passwords do not match", "error");
    try {
      await api.changePassword(curPw, newPw);
      // The server revoked every token; sign straight back in.
      const r = await api.login($user!.email, newPw);
      setToken(r.token);
      curPw = newPw = newPw2 = "";
      toast("password changed");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  function close() {
    ui.settingsTab = null;
  }
  function logout() {
    close();
    api.logout().catch(() => {});
    setToken("");
    user.set(null);
  }
  async function broadcast() {
    const m = alertText.trim();
    if (!m) return;
    try {
      await api.broadcastAlert(m);
      alertText = "";
      toast("alert sent to all users");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  function onKey(e: KeyboardEvent) {
    // Inner editors (skill editor, sheets) handle their own Escape first.
    if (e.key === "Escape" && ui.settingsTab && !document.querySelector(".sheet")) close();
  }
</script>

<svelte:window onkeydown={onKey} />

{#if ui.settingsTab}
  <div class="modal-scrim" role="presentation" onclick={close}></div>
  <div class="modal settings" role="dialog" aria-label="Settings">
    <nav class="snav">
      <div class="stitle">Settings</div>
      {#each tabs as t}
        <button class="nav-item" class:active={ui.settingsTab === t.id} onclick={() => (ui.settingsTab = t.id)}>
          <Icon name={t.icon} /> {t.label}
        </button>
      {/each}
    </nav>
    <div class="spane">
      <button class="icon-btn sclose" onclick={close} aria-label="Close settings"><Icon name="x" /></button>
      {#if ui.settingsTab === "general"}
        <section class="panel"><div class="container col">
          <h3>Account</h3>
          <div class="card col" style="gap:4px;">
            <div class="kv"><b>Email</b><span>{$user?.email}</span></div>
            <div class="kv"><b>Role</b><span>{$user?.role}</span></div>
          </div>
          <h3>Appearance</h3>
          <div class="row">
            {#each themes as t}
              <button class="theme-opt" class:on={theme.pref === t.id} onclick={() => setTheme(t.id)}><Icon name={t.icon} /> {t.label}</button>
            {/each}
          </div>
          <h3>Interface</h3>
          <div class="card row">
            <Icon name="phone" />
            <span class="muted small" style="flex:1;">The touch-friendly layout. Phones open it automatically; your choice is remembered in this browser.</span>
            <button onclick={() => switchUI("mobile")}>Use mobile version</button>
          </div>
          {#if isElevated($user?.role)}
            <h3>Broadcast</h3>
            <div class="row">
              <input placeholder="Alert message to all users" bind:value={alertText} onkeydown={(e) => e.key === "Enter" && broadcast()} />
              <button onclick={broadcast} disabled={!alertText.trim()}><Icon name="megaphone" size={14} /> Send</button>
            </div>
          {/if}
          <div style="margin-top: 12px;"><button class="danger" onclick={logout}><Icon name="logout" size={14} /> Log out</button></div>
        </div></section>
      {:else if ui.settingsTab === "login"}
        <section class="panel"><div class="container col">
          <h3>Change password</h3>
          <form class="col" style="max-width:360px;" onsubmit={(e) => { e.preventDefault(); changePassword(); }}>
            <input type="password" placeholder="Current password" autocomplete="current-password" bind:value={curPw} />
            <input type="password" placeholder="New password (min 8 characters)" autocomplete="new-password" bind:value={newPw} />
            <input type="password" placeholder="Repeat new password" autocomplete="new-password" bind:value={newPw2} />
            <div><button type="submit" disabled={!curPw || !newPw}>Change password</button></div>
          </form>
          <p class="muted small">Other devices and sessions signed in with the old password will be logged out.</p>
        </div></section>
      {:else if ui.settingsTab === "users" && isOwner($user?.role)}
        <Users />
      {:else if ui.settingsTab === "hats"}
        <Hats />
      {:else if ui.settingsTab === "mcp"}
        <Mcp />
      {:else if ui.settingsTab === "config"}
        <Config />
      {:else if ui.settingsTab === "usage"}
        <Usage />
      {:else if ui.settingsTab === "audit" && isOwner($user?.role)}
        <Audit />
      {/if}
    </div>
  </div>
{/if}

<style>
  .settings { width: min(980px, 94vw); height: min(720px, 88vh); flex-direction: row; overflow: hidden; }
  .snav { width: 200px; flex: none; background: var(--sidebar); border-right: 1px solid var(--border-soft); padding: 12px 8px;
    display: flex; flex-direction: column; gap: 1px; border-radius: 14px 0 0 14px; }
  .stitle { font-weight: 700; padding: 4px 8px 10px; }
  .nav-item { justify-content: flex-start; gap: 9px; background: transparent; border: none; box-shadow: none; padding: 5px 8px;
    min-height: 32px; color: var(--muted); }
  .nav-item:hover { background: var(--hover); color: var(--text); }
  .nav-item.active { background: var(--surface); color: var(--text); box-shadow: var(--shadow-1); font-weight: 600; }
  .spane { flex: 1; min-width: 0; display: flex; flex-direction: column; position: relative; }
  .sclose { position: absolute; top: 10px; right: 10px; z-index: 2; }
  .spane :global(.container) { padding-top: 18px; padding-right: 48px; }
  .theme-opt { gap: 6px; }
  .theme-opt.on { border-color: var(--accent-dim); color: var(--accent-text); background: var(--accent-soft); }
</style>
