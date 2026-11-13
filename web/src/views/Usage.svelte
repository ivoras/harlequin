<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast, user } from "../lib/stores";
  import { isElevated, type UsageDay, type UsageUser } from "../lib/types";
  import { daily, weekly, monthly, localToday } from "../lib/usagebuckets";
  import UsageBars from "./UsageBars.svelte";

  // Owners/admins see the whole server's usage (what the provider bills); the
  // per-user tables only ever held one account's calls.
  const elevated = $derived(isElevated($user?.role));
  let scope = $state<"all" | "mine">("all");
  let mine = $state<UsageDay[]>([]);
  let users = $state<UsageUser[]>([]);
  let loading = $state(true);
  const allMode = $derived(elevated && scope === "all");
  const days = $derived(allMode ? mergeDays(users) : mine);
  function mergeDays(us: UsageUser[]): UsageDay[] {
    const by = new Map<string, UsageDay>();
    for (const u of us)
      for (const d of u.days) {
        const t = by.get(d.day) ?? { day: d.day, cost_usd: 0, calls: 0, prompt_tokens: 0, cached_tokens: 0, completion_tokens: 0 };
        t.cost_usd += d.cost_usd;
        t.calls += d.calls;
        t.prompt_tokens += d.prompt_tokens;
        t.cached_tokens += d.cached_tokens;
        t.completion_tokens += d.completion_tokens;
        by.set(d.day, t);
      }
    return [...by.values()].sort((a, b) => a.day.localeCompare(b.day));
  }
  const perUser = $derived(
    users
      .map((u) => {
        const recent = u.days.filter((d) => d.day >= since);
        return {
          email: u.email || `user ${u.user_id}`,
          calls: recent.reduce((s, d) => s + d.calls, 0),
          total: recent.reduce((s, d) => s + d.cost_usd, 0),
          today: recent.filter((d) => d.day === today).reduce((s, d) => s + d.cost_usd, 0),
        };
      })
      .filter((u) => u.calls > 0)
      .sort((a, b) => b.total - a.total),
  );
  const money = (n: number) => `$${n.toFixed(n < 1 ? 4 : 2)}`;
  const today = localToday();
  // First day of the 30-day window (today inclusive); days are YYYY-MM-DD.
  const since = (() => {
    const d = new Date(today + "T00:00:00");
    d.setDate(d.getDate() - 29);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  })();
  const byDay = $derived(daily(days, today, 30));
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
      const tz = -new Date().getTimezoneOffset();
      mine = await api.usageDaily(tz);
      if (elevated) users = await api.usageUsers(tz);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      loading = false;
    }
  }
</script>

<section class="panel">
  <div class="container col">
    <div class="row">
      <h3>Usage{allMode ? " · all users" : ""}</h3>
      <span class="spacer"></span>
      {#if elevated}
        <select id="usage-scope" bind:value={scope} aria-label="Usage scope">
          <option value="all">All users</option>
          <option value="mine">Only mine</option>
        </select>
      {/if}
    </div>
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
      <div class="row"><b class="muted">Cost (all time{allMode ? ", all users" : ""})</b><span class="spacer"></span>${cost.toFixed(cost < 1 ? 4 : 2)}</div>
    </div>
    {#if loading}
      <div class="muted small">Loading…</div>
    {:else if days.length === 0}
      <div class="muted small">No usage recorded yet.</div>
    {:else}
      <UsageBars title="Daily cost — last 30 days" buckets={byDay} barWidth={12} />
      {#if allMode && perUser.length}
        <div class="card">
          <b class="muted">By user — last 30 days</b>
          {#each perUser as u (u.email)}
            <div class="row small">
              <span>{u.email}</span><span class="spacer"></span>
              <span class="muted">{u.calls.toLocaleString()} calls</span>
              <span style="min-width:7ch;text-align:right">{money(u.today)} today</span>
              <b style="min-width:8ch;text-align:right">{money(u.total)}</b>
            </div>
          {/each}
        </div>
      {/if}
      <UsageBars title="Weekly cost — last 52 weeks" buckets={byWeek} barWidth={16} />
      <UsageBars title="Monthly cost — all time" buckets={byMonth} barWidth={36} />
      <div class="muted small">Days follow your local time.</div>
    {/if}
  </div>
</section>
