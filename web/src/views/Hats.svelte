<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { session, toast } from "../lib/stores";
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
    if (!$session.id) return toast("no active conversation", "error");
    try {
      await api.setConversationHat($session.id, h.name);
      toast(`wearing the ${h.name} hat`);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function off() {
    if (!$session.id) return;
    try {
      await api.setConversationHat($session.id, "");
      toast("hat off");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
</script>

<section class="panel">
  <div class="container col">
    <div class="row"><h3>Hats</h3><span class="spacer"></span><button class="small" onclick={off}>No hat</button></div>
    <div class="muted small">A hat sets the system prompt + visible skills for this conversation.</div>
    <div class="list">
      {#each hats as h}
        <div class="card col">
          <div class="row"><strong>{h.name}</strong><span class="spacer"></span><button class="primary small" onclick={() => wear(h)}>Wear</button></div>
          <div class="muted small">{h.description}</div>
          {#if h.skills && h.skills.length}<div class="muted small">skills: {h.skills.join(", ")}</div>{/if}
        </div>
      {/each}
      {#if hats.length === 0}<div class="muted small">No hats defined.</div>{/if}
    </div>
  </div>
</section>
