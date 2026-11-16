<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast } from "../lib/stores";

  let cfg = $state<Record<string, string>>({});
  let key = $state("");
  let val = $state("");

  let tg = $state({ enabled: false, linked: false });
  let tgLink = $state("");

  async function load() {
    // No card when the status is unavailable; the config list must still load.
    tg = await api.telegramStatus().catch(() => ({ enabled: false, linked: false }));
    if (tg.linked) tgLink = "";
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
  async function tgConnect() {
    try {
      tgLink = (await api.telegramLink()).url;
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function tgDisconnect() {
    try {
      await api.telegramUnlink();
      await load();
      toast("Telegram disconnected");
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

<!-- Linking happens in Telegram; coming back to this tab shows the result. -->
<svelte:window onfocus={load} />

<section class="panel">
  <div class="container col">
    {#if tg.enabled}
      <h3>Telegram</h3>
      <div class="card col">
        {#if tg.linked}
          <div class="row">
            <span style="flex:1;">Your Telegram chat is connected.</span>
            <button class="ghost danger small" onclick={tgDisconnect}>Disconnect</button>
          </div>
        {:else if tgLink}
          <div>Open this link in Telegram and press <strong>Start</strong>. It works once and expires in 10 minutes.</div>
          <a class="wrap" href={tgLink} target="_blank" rel="noopener">{tgLink}</a>
        {:else}
          <div class="muted small">Talk to Harlequin from Telegram and get notifications there.</div>
          <button class="primary" onclick={tgConnect}>Connect Telegram</button>
        {/if}
      </div>
    {/if}
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
