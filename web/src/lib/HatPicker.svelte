<script lang="ts">
  // One card per hat for the empty-session screen: click to wear, click the worn
  // one again to take it off. Renders nothing when there are no hats.
  import { onMount } from "svelte";
  import { api } from "./api";
  import { session, wornHat, toast } from "./stores";
  import { wearableHats } from "./hats";
  import type { Hat } from "./types";

  let hats = $state<Hat[]>([]);
  onMount(() => {
    api.listHats().then((l) => (hats = wearableHats(l))).catch(() => {});
  });

  async function pick(h: Hat) {
    if (!$session.id) return;
    const name = $wornHat === h.name ? "" : h.name;
    try {
      await api.setSessionHat($session.id, name);
      wornHat.set(name);
      toast(name ? `wearing the ${name} hat` : "hat off");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
</script>

{#if hats.length}
  <div class="hatpicker" role="group" aria-label="Hats">
    <div class="muted small">Work in a mode — wear a hat:</div>
    <div class="cards">
      {#each hats as h (h.name)}
        <button class="hatcard" class:worn={$wornHat === h.name} aria-pressed={$wornHat === h.name} onclick={() => pick(h)} title={h.description}>
          <strong>🎩 {h.name}{#if h.scope === "personal"} <span class="pill">personal</span>{/if}</strong>
          <span class="muted small">{h.description}</span>
        </button>
      {/each}
    </div>
  </div>
{/if}

<style>
  .hatpicker { margin-top: 18px; display: flex; flex-direction: column; align-items: center; gap: 8px; }
  .cards { display: flex; flex-wrap: wrap; justify-content: center; gap: 8px; }
  .hatcard { display: flex; flex-direction: column; align-items: flex-start; gap: 2px; text-align: left; max-width: 260px;
    padding: 8px 12px; border-radius: 10px; background: var(--surface-2); border: 1px solid var(--border); }
  .hatcard.worn { border-color: var(--accent); background: var(--accent-soft, var(--surface-3)); }
</style>
