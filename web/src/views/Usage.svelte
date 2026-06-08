<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast } from "../lib/stores";
  import type { UsageRecord } from "../lib/types";

  let rows = $state<UsageRecord[]>([]);
  let total = $derived(rows.reduce((s, r) => s + r.est_cost_usd, 0));
  let pt = $derived(rows.reduce((s, r) => s + r.prompt_tokens, 0));
  let ct = $derived(rows.reduce((s, r) => s + r.completion_tokens, 0));

  onMount(async () => {
    try {
      rows = await api.usage();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  });
</script>

<section class="panel">
  <div class="container col">
    <h3>Usage</h3>
    <div class="card">
      <div class="row"><b class="muted">Records</b><span class="spacer"></span>{rows.length}</div>
      <div class="row"><b class="muted">Prompt tokens</b><span class="spacer"></span>{pt.toLocaleString()}</div>
      <div class="row"><b class="muted">Completion tokens</b><span class="spacer"></span>{ct.toLocaleString()}</div>
      <div class="row"><b class="muted">Est. cost</b><span class="spacer"></span>${total.toFixed(4)}</div>
    </div>
    <div class="list">
      {#each rows.slice(0, 100) as r}
        <div class="card small">
          <div class="row"><strong>{r.model}</strong><span class="spacer"></span><span class="muted">{new Date(r.created_at).toLocaleString()}</span></div>
          <div class="muted">{r.provider} · {r.prompt_tokens}+{r.completion_tokens} tok · ${r.est_cost_usd.toFixed(4)}</div>
        </div>
      {/each}
      {#if rows.length === 0}<div class="muted small">No usage recorded.</div>{/if}
    </div>
  </div>
</section>
