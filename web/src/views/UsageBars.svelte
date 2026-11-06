<script lang="ts">
  // A horizontally scrollable cost histogram: one bar per bucket, newest on the
  // right and scrolled into view on load. Hovering or focusing a bar shows its
  // figures in the readout line; the newest bucket is shown otherwise.
  import type { Bucket } from "../lib/usagebuckets";

  let { title, buckets, barWidth = 12 }: { title: string; buckets: Bucket[]; barWidth?: number } = $props();

  let scroller = $state<HTMLDivElement | null>(null);
  let active = $state<Bucket | null>(null);
  const max = $derived(buckets.reduce((m, b) => Math.max(m, b.cost), 0));
  const total = $derived(buckets.reduce((s, b) => s + b.cost, 0));
  const shown = $derived(active ?? buckets[buckets.length - 1] ?? null);
  const H = 120; // plot height in px

  // Start at the latest bucket (re-applied when the data changes).
  $effect(() => {
    void buckets;
    if (scroller) queueMicrotask(() => scroller && (scroller.scrollLeft = scroller.scrollWidth));
  });

  const usd = (v: number) => (v === 0 ? "$0" : "$" + v.toFixed(v < 0.01 ? 4 : v < 1 ? 3 : 2));
  const tok = (n: number) => (n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(1) + "k" : String(n));
  const describe = (b: Bucket) =>
    `${b.label}: ${usd(b.cost)}` + (b.calls ? ` · ${b.calls} call${b.calls === 1 ? "" : "s"} · ${tok(b.prompt)} prompt (${b.prompt ? Math.round((b.cached * 100) / b.prompt) : 0}% cached) · ${tok(b.completion)} generated` : "");
</script>

<section class="ub">
  <div class="ub-head">
    <h4>{title}</h4>
    <span class="ub-total">{usd(total)} total</span>
  </div>
  <div class="ub-readout" aria-live="polite">{shown ? describe(shown) : "No data"}</div>
  <div class="ub-plot">
    <div class="ub-axis" style="height:{H}px">
      <span>{usd(max)}</span>
      <span>$0</span>
    </div>
    <div class="ub-scroll" bind:this={scroller} role="group" aria-label={title}>
      <div class="ub-bars" style="height:{H}px">
        {#each buckets as b (b.key)}
          <button class="ub-col" style="width:{barWidth}px" aria-label={describe(b)}
            onmouseenter={() => (active = b)} onmouseleave={() => (active = null)}
            onfocus={() => (active = b)} onblur={() => (active = null)}>
            <span class="ub-bar" class:on={active === b}
              style="height:{max > 0 && b.cost > 0 ? Math.max(2, (b.cost / max) * H) : 0}px"></span>
          </button>
        {/each}
      </div>
      <div class="ub-ticks">
        {#each buckets as b (b.key)}
          <span class="ub-tick" style="width:{barWidth}px">{b.tick}</span>
        {/each}
      </div>
    </div>
  </div>
</section>

<style>
  .ub { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
  .ub-head { display: flex; align-items: baseline; gap: 8px; }
  .ub-head h4 { margin: 0; font-size: 13px; }
  .ub-total { margin-left: auto; font-size: 12px; color: var(--muted); }
  .ub-readout { font-size: 12px; color: var(--muted); min-height: 1.4em; overflow-wrap: anywhere; }
  .ub-plot { display: flex; gap: 6px; min-width: 0; }
  .ub-axis { display: flex; flex-direction: column; justify-content: space-between; font-size: 10.5px;
    color: var(--faint, var(--muted)); text-align: right; flex: none; min-width: 3.2em; }
  .ub-scroll { overflow-x: auto; overflow-y: hidden; flex: 1; min-width: 0; padding-bottom: 2px; }
  .ub-bars { display: flex; align-items: flex-end; gap: 2px; border-bottom: 1px solid var(--border-soft);
    background: linear-gradient(var(--border-soft), var(--border-soft)) top / 100% 1px no-repeat; width: max-content; }
  /* The column is the hit target (full height), the bar inside is the mark. */
  .ub-col { height: 100%; padding: 0; margin: 0; border: 0; background: none; box-shadow: none; min-height: 0;
    display: flex; align-items: flex-end; cursor: default; flex: none; border-radius: 0; }
  .ub-col:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
  .ub-bar { display: block; width: 100%; background: var(--accent); border-radius: 4px 4px 0 0; opacity: 0.8; }
  .ub-bar.on { opacity: 1; }
  .ub-ticks { display: flex; gap: 2px; width: max-content; }
  .ub-tick { flex: none; font-size: 10.5px; color: var(--faint, var(--muted)); white-space: nowrap; overflow: visible; height: 1.3em; }
</style>
