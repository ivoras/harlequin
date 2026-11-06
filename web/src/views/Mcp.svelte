<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast, user } from "../lib/stores";
  import { authorizeMCP } from "../lib/workspace";
  import { isElevated, type MCPServer, type RegisterMCPRequest } from "../lib/types";

  let servers = $state<MCPServer[]>([]);
  let loading = $state(true);
  let disabled = $state(false); // MCP client turned off server-side (routes absent)
  let showAdd = $state(false);
  let scopesText = $state("");
  // Header auth rows; "Authorization: Bearer <key>" is by far the common case.
  const blankHeaders = () => [{ name: "Authorization", value: "" }];
  let headers = $state(blankHeaders());
  const blank = (): RegisterMCPRequest => ({ scope: "user", name: "", url: "", auth_type: "none" });
  let f = $state<RegisterMCPRequest>(blank());
  const elevated = $derived(isElevated($user?.role));

  async function load() {
    try {
      servers = await api.listMCP();
      disabled = false;
    } catch (e) {
      const msg = (e as Error).message;
      if (msg.startsWith("404")) disabled = true;
      else toast(msg, "error");
    } finally {
      loading = false;
    }
  }
  onMount(load);
  async function add() {
    const req: RegisterMCPRequest = { ...f, name: f.name.trim(), url: f.url.trim() };
    if (req.auth_type === "oauth") {
      const sc = scopesText.split(/[\s,]+/).filter(Boolean);
      if (sc.length) req.oauth_scopes = sc;
    } else if (req.auth_type === "header") {
      req.headers = headers.map((h) => ({ name: h.name.trim(), value: h.value.trim() })).filter((h) => h.name && h.value);
      if (!req.headers.length) return toast("enter a header name and value", "error");
    }
    try {
      await api.registerMCP(req);
      showAdd = false;
      f = blank();
      scopesText = "";
      headers = blankHeaders();
      // An OAuth server is useless until authorized: go straight to consent.
      if (req.auth_type === "oauth") return authorizeMCP(req.scope, req.name);
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
      await load();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function toggle(s: MCPServer) {
    try {
      await api.updateMCP(s.scope, s.name, { scope: s.scope, name: s.name, url: "", auth_type: "", enabled: !s.enabled });
      await load();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  // The update API replaces the whole header set, so ask for each stored name.
  async function rekey(s: MCPServer) {
    const hs = [];
    for (const name of s.header_names?.length ? s.header_names : ["Authorization"]) {
      const value = prompt(`New value for header "${name}" on ${s.name}`)?.trim();
      if (!value) return;
      hs.push({ name, value });
    }
    try {
      await api.updateMCP(s.scope, s.name, { scope: s.scope, name: s.name, url: "", auth_type: "header", headers: hs });
      await load();
      toast("key updated");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function rm(s: MCPServer) {
    if (!confirm(`Remove MCP server "${s.name}"?`)) return;
    try {
      await api.deleteMCP(s.scope, s.name);
      servers = servers.filter((x) => !(x.scope === s.scope && x.name === s.name));
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  // Shared servers are admin-managed; users can still authorize/test them.
  const canEdit = (s: MCPServer) => s.scope === "user" || elevated;
</script>

<section class="panel">
  <div class="container col">
    <div class="row">
      <h3>MCP servers</h3><span class="spacer"></span>
      {#if !disabled}<button class="primary small" onclick={() => (showAdd = !showAdd)}>+ Add</button>{/if}
    </div>
    {#if disabled}
      <div class="muted small">MCP is turned off on this server. An administrator can enable it with <code>mcp.enabled: true</code> in the server config.</div>
    {:else}
      <div class="muted small">Tools from enabled, connected servers are available to the assistant in every chat.</div>
      {#if showAdd}
        <div class="card col">
          {#if elevated}
            <select bind:value={f.scope}><option value="user">Just me</option><option value="shared">Everyone in the organisation</option></select>
          {/if}
          <input placeholder="name (letters, digits, . _ -)" bind:value={f.name} />
          <input placeholder="server URL (https://…/mcp)" bind:value={f.url} inputmode="url" />
          <select bind:value={f.auth_type}><option value="none">No authentication</option><option value="oauth">OAuth (sign in with the provider)</option><option value="header">API key / header</option></select>
          {#if f.auth_type === "header"}
            {#each headers as h, i}
              <div class="row" style="gap:6px;">
                <input placeholder="header name" bind:value={h.name} style="flex:1;" />
                <input type="password" autocomplete="off" placeholder={h.name.toLowerCase() === "authorization" ? "Bearer <your API key>" : "value"} bind:value={h.value} style="flex:2;" />
                {#if headers.length > 1}<button class="ghost small" aria-label="Remove header" onclick={() => headers.splice(i, 1)}>✕</button>{/if}
              </div>
            {/each}
            <button class="ghost small" onclick={() => headers.push({ name: "", value: "" })}>+ Another header</button>
            <div class="muted small">Sent on every request to this server; stored encrypted.</div>
          {/if}
          {#if f.auth_type === "oauth"}
            <input placeholder="scopes (optional, space-separated)" bind:value={scopesText} />
            <div class="muted small">After registering you'll be sent to the provider to sign in.</div>
          {/if}
          <button class="primary" onclick={add} disabled={!f.name.trim() || !f.url.trim()}>Register</button>
        </div>
      {/if}
      <div class="list">
        {#each servers as s (s.scope + "/" + s.name)}
          <div class="card col">
            <div class="row">
              <strong>{s.name}</strong><span class="pill">{s.scope}</span>
              {#if !s.enabled}<span class="pill">disabled</span>
              {:else if s.needs_auth}<span class="warn small">needs sign-in</span>
              {:else if s.error}<span class="small" style="color:var(--danger);">unreachable</span>
              {:else if s.auth_satisfied}<span class="pill">{s.tool_count ?? 0} tools</span>{/if}
            </div>
            <div class="muted small wrap">{s.url}</div>
            {#if s.error}<div class="small wrap" style="color:var(--danger);">{s.error}</div>{/if}
            {#if s.tools?.length}
              <details>
                <summary class="small muted">Tools</summary>
                {#each s.tools as t}
                  <div class="small"><code>{t.name}</code>{#if t.description}<span class="muted"> — {t.description}</span>{/if}</div>
                {/each}
              </details>
            {/if}
            <div class="row small" style="gap:6px; flex-wrap:wrap;">
              {#if s.auth_type === "oauth"}
                <button class={s.needs_auth ? "primary" : ""} onclick={() => authorizeMCP(s.scope, s.name)}>{s.needs_auth ? "Sign in" : "Re-authorize"}</button>
              {/if}
              <button onclick={() => test(s)}>Test</button>
              {#if s.auth_type === "header" && canEdit(s)}<button onclick={() => rekey(s)}>Change key</button>{/if}
              {#if canEdit(s)}<button onclick={() => toggle(s)}>{s.enabled ? "Disable" : "Enable"}</button>{/if}
              <span class="spacer"></span>
              {#if canEdit(s)}<button class="ghost danger" onclick={() => rm(s)}>Remove</button>{/if}
            </div>
          </div>
        {/each}
        {#if !loading && servers.length === 0}<div class="muted small">No MCP servers yet.</div>{/if}
      </div>
    {/if}
  </div>
</section>
