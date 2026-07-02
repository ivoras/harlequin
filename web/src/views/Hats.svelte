<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { session, wornHat, toast } from "../lib/stores";
  import type { Hat } from "../lib/types";

  let hats = $state<Hat[]>([]);
  onMount(async () => {
    try {
      hats = await api.listHats();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  });
  async function wear(h: Hat) {
    if (!$session.id) return toast("no active session", "error");
    try {
      await api.setSessionHat($session.id, h.name);
      wornHat.set(h.name);
      toast(`wearing the ${h.name} hat`);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function off() {
    if (!$session.id) return;
    try {
      await api.setSessionHat($session.id, "");
      wornHat.set("");
      toast("hat off");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
</script>

<section class="panel">
  <div class="container col">
    <div class="row"><h3>Hats</h3><span class="spacer"></span>
      <button class="small" onclick={off} disabled={!$wornHat}>Take off{$wornHat ? ` (${$wornHat})` : ""}</button></div>
    <div class="muted small">A hat sets the system prompt + visible skills for this session.</div>
    <div class="list">
      {#each hats as h}
        <div class="card col">
          <div class="row"><strong>{h.name}</strong>
            {#if $wornHat === h.name}<span class="pill" style="color: var(--accent);">worn</span>{/if}
            <span class="spacer"></span>
            {#if $wornHat === h.name}
              <button class="small" onclick={off}>Take off</button>
            {:else}
              <button class="primary small" onclick={() => wear(h)}>Wear</button>
            {/if}
          </div>
          <div class="muted small">{h.description}</div>
          {#if h.skills && h.skills.length}<div class="muted small">skills: {h.skills.join(", ")}</div>{/if}
        </div>
      {/each}
      {#if hats.length === 0}<div class="muted small">No hats defined.</div>{/if}
    </div>
  </div>
</section>
