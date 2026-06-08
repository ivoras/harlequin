<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast } from "../lib/stores";

  let cfg = $state<Record<string, string>>({});
  let key = $state("");
  let val = $state("");

  async function load() {
    try {
      cfg = await api.getConfig();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  onMount(load);

  async function set() {
    if (!key.trim()) return;
    try {
      await api.setConfig(key.trim(), val);
      key = "";
      val = "";
      await load();
      toast("saved");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function del(k: string) {
    try {
      await api.deleteConfig(k);
      await load();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
</script>

<section class="panel">
  <div class="container col">
    <h3>Config</h3>
    <div class="muted small">Per-user key/value — e.g. <code>telegram.chat_id</code>.</div>
    <div class="list">
      {#each Object.entries(cfg) as [k, v]}
        <div class="card row">
          <div class="col" style="flex:1; min-width:0; gap:1px;">
            <strong>{k}</strong><span class="muted small wrap">{v}</span>
          </div>
          <button class="ghost danger small" onclick={() => del(k)} aria-label="Delete">✕</button>
        </div>
      {/each}
      {#if Object.keys(cfg).length === 0}<div class="muted small">No config set.</div>{/if}
    </div>
    <div class="card col">
      <input placeholder="key (e.g. telegram.chat_id)" bind:value={key} />
      <input placeholder="value" bind:value={val} />
      <button class="primary" onclick={set} disabled={!key.trim()}>Set</button>
    </div>
  </div>
</section>
