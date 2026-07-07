<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast, session, activeProject, lastModel } from "../lib/stores";
  import type { ContextBreakdown } from "../lib/types";

  let breakdown = $state<ContextBreakdown | null>(null);
  let loading = $state(true);

  function fmtk(n: number): string {
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
    if (n >= 1000) return (n / 1000).toFixed(1) + "k";
    return String(n);
  }

  function pct(tokens: number, max: number): number {
    if (max <= 0) return 0;
    return Math.min(100, (tokens * 100) / max);
  }

  onMount(async () => {
    try {
      breakdown = await api.getContextBreakdown($session.id, $activeProject?.id ?? 0, $lastModel);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      loading = false;
    }
  });
</script>

<section class="panel">
  <div class="container col">
    <h3>Context usage</h3>
    {#if loading}
      <div class="muted small">Loading…</div>
    {:else if !breakdown}
      <div class="muted small">No data.</div>
    {:else}
      <div class="card">
        <div class="row">
          <b class="muted">Model</b><span class="spacer"></span>{breakdown.model || "—"}
        </div>
        <div class="row">
          <b class="muted">Total</b><span class="spacer"></span>
          {fmtk(breakdown.total)}{breakdown.context_max ? ` / ${fmtk(breakdown.context_max)}` : ""} tokens
        </div>
      </div>
      <div class="list">
        {#each breakdown.categories as c}
          <div class="card small">
            <div class="row">
              <span>{c.name}</span><span class="spacer"></span>
              <span class="muted">{fmtk(c.tokens)} tok{breakdown.context_max ? ` (${pct(c.tokens, breakdown.context_max).toFixed(1)}%)` : ""}</span>
            </div>
            {#if breakdown.context_max}
              <div class="meter"><div class="meter-fill" style="width: {pct(c.tokens, breakdown.context_max)}%"></div></div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </div>
</section>

<style>
  .meter {
    height: 6px;
    border-radius: 3px;
    background: color-mix(in srgb, currentColor 12%, transparent);
    overflow: hidden;
    margin-top: 6px;
  }
  .meter-fill {
    height: 100%;
    background: color-mix(in srgb, currentColor 60%, transparent);
  }
</style>
