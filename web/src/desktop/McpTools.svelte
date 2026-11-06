<script lang="ts">
  // Sidebar list of the MCP tools the assistant can use, grouped by server.
  // Configuration lives in Settings → MCP servers (the gear button).
  import { api } from "../lib/api";
  import { authorizeMCP } from "../lib/workspace";
  import type { MCPServer } from "../lib/types";
  import Icon from "./Icon.svelte";
  import { ui } from "./state.svelte";

  let servers = $state<MCPServer[]>([]);
  let disabled = $state(false);
  let open = $state(false);
  let expanded = $state<Record<string, boolean>>({});

  async function load() {
    try {
      servers = (await api.listMCP()).filter((s) => s.enabled);
      disabled = false;
    } catch (e) {
      // 404: MCP client off server-side, hide the section; other errors are transient.
      if ((e as Error).message.startsWith("404")) disabled = true;
    }
  }
  // Load on mount and again whenever Settings closes (servers may have changed).
  $effect(() => {
    if (ui.settingsTab === null) load();
  });
  const toolCount = $derived(servers.reduce((n, s) => n + (s.tools?.length ?? 0), 0));
  const key = (s: MCPServer) => s.scope + "/" + s.name;
</script>

{#if !disabled}
  <div class="mcp">
    <div class="mcp-head">
      <button class="nav-item" onclick={() => (open = !open)} aria-expanded={open}>
        <Icon name="plug" /> <span class="grow">MCP tools</span>
        {#if servers.some((s) => s.needs_auth)}<span class="dot warn" title="A server needs sign-in"></span>{/if}
        {#if toolCount}<span class="faint small">{toolCount}</span>{/if}
        <span class="chev" class:open><Icon name="chevronRight" size={12} /></span>
      </button>
      <button class="icon-btn cfg" onclick={() => ui.openSettings("mcp")} title="Configure MCP servers" aria-label="Configure MCP servers">
        <Icon name="settings" size={14} />
      </button>
    </div>
    {#if open}
      <div class="mcp-list">
        {#each servers as s (key(s))}
          <button class="srv" onclick={() => (expanded[key(s)] = !expanded[key(s)])} title={s.error || s.url}>
            <span class="chev" class:open={expanded[key(s)]}><Icon name="chevronRight" size={11} /></span>
            <span class="grow">{s.name}</span>
            {#if s.needs_auth}<span class="dot warn"></span>{:else if s.error}<span class="dot err"></span>{:else}<span class="faint small">{s.tools?.length ?? 0}</span>{/if}
          </button>
          {#if expanded[key(s)]}
            {#if s.needs_auth}
              <button class="tool link" onclick={() => authorizeMCP(s.scope, s.name)}>Sign in to use its tools…</button>
            {:else if s.error}
              <div class="tool err-text" title={s.error}>Unreachable</div>
            {:else}
              {#each s.tools ?? [] as t}
                <div class="tool" title={t.description || t.name}>{t.name}</div>
              {:else}
                <div class="tool faint">No tools</div>
              {/each}
            {/if}
          {/if}
        {:else}
          <button class="tool link" onclick={() => ui.openSettings("mcp")}>Add an MCP server…</button>
        {/each}
      </div>
    {/if}
  </div>
{/if}

<style>
  .mcp-head { display: flex; align-items: center; position: relative; }
  .nav-item { width: 100%; justify-content: flex-start; gap: 9px; background: transparent; border: none;
    box-shadow: none; padding: 5px 30px 5px 8px; min-height: 30px; color: var(--muted); border-radius: 6px; }
  .nav-item:hover { background: var(--hover); color: var(--text); }
  .cfg { position: absolute; right: 2px; padding: 3px; }
  .grow { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; text-align: left; }
  .chev { display: inline-flex; transition: transform 0.12s; color: var(--faint); }
  .chev.open { transform: rotate(90deg); }
  .mcp-list { display: flex; flex-direction: column; max-height: 30vh; overflow-y: auto; padding-left: 18px; }
  .srv, .tool { width: 100%; justify-content: flex-start; gap: 6px; background: transparent; border: none; box-shadow: none;
    padding: 3px 8px; min-height: 24px; font-size: 13px; color: var(--muted); border-radius: 6px; text-align: left; }
  .srv:hover, .tool.link:hover { background: var(--hover); color: var(--text); }
  .tool { display: block; padding-left: 25px; font-size: 12px; font-family: var(--mono, monospace);
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tool.link { font-family: inherit; color: var(--accent-text); cursor: pointer; }
  .err-text { color: var(--danger); font-family: inherit; }
  .dot { width: 7px; height: 7px; border-radius: 50%; flex: none; }
  .dot.warn { background: var(--warn, orange); }
  .dot.err { background: var(--danger); }
</style>
