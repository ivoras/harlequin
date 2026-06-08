<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast } from "../lib/stores";
  import type { MCPServer, RegisterMCPRequest } from "../lib/types";

  let servers = $state<MCPServer[]>([]);
  let showAdd = $state(false);
  const blank = (): RegisterMCPRequest => ({ scope: "user", name: "", url: "", auth_type: "none" });
  let f = $state<RegisterMCPRequest>(blank());

  async function load() {
    try {
      servers = await api.listMCP();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  onMount(load);
  async function add() {
    try {
      await api.registerMCP({ ...f });
      showAdd = false;
      f = blank();
      await load();
      toast("registered");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function test(s: MCPServer) {
    try {
      const r = await api.testMCP(s.scope, s.name);
      toast(r.ok ? `connected: ${(r.tools || []).join(", ") || "no tools"}` : `failed: ${r.error}`, r.ok ? "info" : "error");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function authorize(s: MCPServer) {
    try {
      const r = await api.startMCPOAuth(s.scope, s.name);
      window.open(r.authorize_url, "_blank", "noopener");
      toast("opened the authorize URL");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function rm(s: MCPServer) {
    try {
      await api.deleteMCP(s.scope, s.name);
      servers = servers.filter((x) => !(x.scope === s.scope && x.name === s.name));
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
</script>

<section class="panel">
  <div class="container col">
    <div class="row"><h3>MCP servers</h3><span class="spacer"></span><button class="primary small" onclick={() => (showAdd = !showAdd)}>+ Add</button></div>
    {#if showAdd}
      <div class="card col">
        <select bind:value={f.scope}><option value="user">user</option><option value="shared">shared (admin)</option></select>
        <input placeholder="name" bind:value={f.name} />
        <input placeholder="url (https://…)" bind:value={f.url} />
        <select bind:value={f.auth_type}><option value="none">no auth</option><option value="oauth">oauth</option></select>
        <button class="primary" onclick={add} disabled={!f.name.trim() || !f.url.trim()}>Register</button>
      </div>
    {/if}
    <div class="list">
      {#each servers as s}
        <div class="card col">
          <div class="row">
            <strong>{s.name}</strong><span class="pill">{s.scope}</span>
            {#if s.tool_count}<span class="pill">{s.tool_count} tools</span>{/if}
            <span class="spacer"></span>{#if s.needs_auth}<span class="warn small">needs auth</span>{/if}
          </div>
          <div class="muted small wrap">{s.url}</div>
          {#if s.error}<div class="small" style="color:var(--danger);">{s.error}</div>{/if}
          <div class="row small" style="gap:6px;">
            <button onclick={() => test(s)}>Test</button>
            {#if s.auth_type === "oauth"}<button onclick={() => authorize(s)}>Authorize</button>{/if}
            <span class="spacer"></span><button class="ghost danger" onclick={() => rm(s)}>Remove</button>
          </div>
        </div>
      {/each}
      {#if servers.length === 0}<div class="muted small">No MCP servers.</div>{/if}
    </div>
  </div>
</section>
