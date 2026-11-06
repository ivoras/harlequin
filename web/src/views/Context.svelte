<script lang="ts">
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

  // Small per-session costs need more than cents to be readable.
  function fmtUSD(v: number): string {
    if (v === 0) return "$0";
    return "$" + v.toFixed(v < 0.01 ? 4 : v < 1 ? 3 : 2);
  }

  function pct(tokens: number, max: number): number {
    if (max <= 0) return 0;
    return Math.min(100, (tokens * 100) / max);
  }

  // Re-fetch whenever the session changes. On a page refresh this view mounts
  // before the boot code has restored the session, so id 0 means "not yet":
  // wait rather than request /sessions/0/context (404 -> error toast).
  $effect(() => {
    const id = $session.id;
    const pid = $activeProject?.id ?? 0;
    const model = $lastModel;
    if (!id) return;
    loading = true;
    api
      .getContextBreakdown(id, pid, model)
      .then((b) => (breakdown = b))
      .catch((e) => toast((e as Error).message, "error"))
      .finally(() => (loading = false));
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
        {#if breakdown.cost?.calls}
          {@const c = breakdown.cost}
          <div class="row" title="{c.calls} LLM calls · {fmtk(c.prompt_tokens)} prompt tokens ({c.prompt_tokens ? Math.round((c.cached_tokens * 100) / c.prompt_tokens) : 0}% cached) · {fmtk(c.completion_tokens)} generated">
            <b class="muted">Session cost</b><span class="spacer"></span>
            {c.reported_calls < c.calls ? "≈ " : ""}{fmtUSD(c.usd)}
          </div>
          <div class="muted small">
            {c.calls} LLM call{c.calls === 1 ? "" : "s"} · {c.prompt_tokens ? Math.round((c.cached_tokens * 100) / c.prompt_tokens) : 0}% of prompt cached{c.reported_calls < c.calls ? ` · ${c.calls - c.reported_calls} estimated from configured prices` : ""}
          </div>
        {/if}
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
