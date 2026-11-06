<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast } from "../lib/stores";
  import type { UsageDay } from "../lib/types";
  import { daily, weekly, monthly, localToday } from "../lib/usagebuckets";
  import UsageBars from "./UsageBars.svelte";

  let days = $state<UsageDay[]>([]);
  let loading = $state(true);
  const today = localToday();
  const byDay = $derived(daily(days, today, 90));
  const byWeek = $derived(weekly(days, today, 52));
  const byMonth = $derived(monthly(days, today));
  const sum = (k: keyof Omit<UsageDay, "day">) => days.reduce((s, d) => s + d[k], 0);
  const cost = $derived(sum("cost_usd"));
  const calls = $derived(sum("calls"));
  const prompt = $derived(sum("prompt_tokens"));
  const cached = $derived(sum("cached_tokens"));
  const completion = $derived(sum("completion_tokens"));

  // Live connections, refreshed while the tab is open.
  let live = $state<Awaited<ReturnType<typeof api.usageLive>> | null>(null);
  async function loadLive() {
    try {
      live = await api.usageLive();
    } catch {
      /* transient: keep the last figures */
    }
  }

  onMount(() => {
    loadLive();
    const t = setInterval(loadLive, 15000);
    loadDays();
    return () => clearInterval(t);
  });

  async function loadDays() {
    try {
      days = await api.usageDaily(-new Date().getTimezoneOffset());
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      loading = false;
    }
  }
</script>

<section class="panel">
  <div class="container col">
    <h3>Usage</h3>
    {#if live}
      <div class="card">
        <div class="row">
          <b class="muted">{live.scope === "server" ? "Active sessions now (all users)" : "Your active sessions now"}</b>
          <span class="spacer"></span>{live.live.sessions}
        </div>
        <div class="muted small">
          {live.live.connections} open connection{live.live.connections === 1 ? "" : "s"}{live.live.running_turns ? ` · ${live.live.running_turns} running a turn` : ""}
          · sessions with at least one connected client; refreshes every 15 s
        </div>
      </div>
    {/if}
    <div class="card">
      <div class="row"><b class="muted">LLM calls</b><span class="spacer"></span>{calls.toLocaleString()}</div>
      <div class="row"><b class="muted">Prompt tokens</b><span class="spacer"></span>{prompt.toLocaleString()}{prompt ? ` (${Math.round((cached * 100) / prompt)}% cached)` : ""}</div>
      <div class="row"><b class="muted">Generated tokens</b><span class="spacer"></span>{completion.toLocaleString()}</div>
      <div class="row"><b class="muted">Cost (all time)</b><span class="spacer"></span>${cost.toFixed(cost < 1 ? 4 : 2)}</div>
    </div>
    {#if loading}
      <div class="muted small">Loading…</div>
    {:else if days.length === 0}
      <div class="muted small">No usage recorded yet.</div>
    {:else}
      <UsageBars title="Daily cost — last 90 days" buckets={byDay} barWidth={12} />
      <UsageBars title="Weekly cost — last 52 weeks" buckets={byWeek} barWidth={16} />
      <UsageBars title="Monthly cost — all time" buckets={byMonth} barWidth={36} />
      <div class="muted small">Costs are the provider-reported figure where available, otherwise estimated from configured model prices. Days follow your local time.</div>
    {/if}
  </div>
</section>
