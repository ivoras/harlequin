<script lang="ts">
  // Owner audit explorer: search + filter the cross-user audit log, with
  // counts by action / user / day for the same filter. Clicking a breakdown
  // row narrows the filter to it.
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast } from "../lib/stores";
  import type { AuditEntry, AuditStats } from "../lib/types";

  const ranges = [
    { id: "24h", label: "Last 24 hours", ms: 864e5 },
    { id: "7d", label: "Last 7 days", ms: 7 * 864e5 },
    { id: "30d", label: "Last 30 days", ms: 30 * 864e5 },
    { id: "all", label: "All time", ms: 0 },
  ];
  let q = $state("");
  let range = $state("7d");
  let action = $state("");
  let userSel = $state("");
  let limit = $state(200);
  let entries = $state<AuditEntry[]>([]);
  let stats = $state<AuditStats | null>(null);
  let emails = $state<Record<number, string>>({});
  let loading = $state(true);
  let seq = 0;

  function params(withLimit: boolean): URLSearchParams {
    const p = new URLSearchParams();
    if (q.trim()) p.set("q", q.trim());
    if (action) p.append("action", action);
    if (userSel) p.set("user", userSel);
    const ms = ranges.find((r) => r.id === range)?.ms;
    if (ms) p.set("from", new Date(Date.now() - ms).toISOString());
    p.set("tz", String(-new Date().getTimezoneOffset()));
    if (withLimit) p.set("limit", String(limit));
    return p;
  }

  async function load() {
    const my = ++seq; // drop out-of-order responses while typing
    loading = true;
    try {
      const [e, s] = await Promise.all([api.auditSearch(params(true)), api.auditStats(params(false))]);
      if (my !== seq) return;
      entries = e;
      stats = s;
    } catch (e) {
      if (my === seq) toast((e as Error).message, "error");
    } finally {
      if (my === seq) loading = false;
    }
  }
  onMount(async () => {
    try {
      emails = Object.fromEntries((await api.listUsers()).users?.map((u) => [u.id, u.email]) ?? []);
    } catch {
      /* ids shown instead */
    }
    load();
  });

  let timer: ReturnType<typeof setTimeout>;
  function changed() {
    limit = 200;
    clearTimeout(timer);
    timer = setTimeout(load, 250);
  }
  function pick(kind: "action" | "user", v: string) {
    if (kind === "action") action = action === v ? "" : v;
    else userSel = userSel === v ? "" : v;
    changed();
  }

  const maxOf = (a: { count: number }[]) => a.reduce((m, x) => Math.max(m, x.count), 1);
  const who = (e: AuditEntry) => (e.user_id != null ? (emails[e.user_id] ?? `user ${e.user_id}`) : "");
  // Entries logged before emails were recorded say "user N": resolve it, or flag it as deleted.
  const target = (t: string) =>
    t.replace(/(^|\s)user (\d+)(?!\d)(?! \()/g, (_, sp, id) => `${sp}${emails[+id] ? `${emails[+id]} (user ${id})` : `user ${id} (deleted account)`}`);
  const when = (s: string) => new Date(s).toLocaleString();
  // Fill gaps so the day chart is a continuous timeline.
  const days = $derived.by(() => {
    const d = stats?.days ?? [];
    if (d.length < 2) return d;
    const m = new Map(d.map((x) => [x.key, x.count]));
    const out: { key: string; count: number }[] = [];
    for (let t = new Date(d[0].key + "T00:00:00Z"); t <= new Date(d[d.length - 1].key + "T00:00:00Z"); t = new Date(t.getTime() + 864e5)) {
      const k = t.toISOString().slice(0, 10);
      out.push({ key: k, count: m.get(k) ?? 0 });
    }
    return out;
  });

  function exportCsv() {
    const esc = (v: string) => `"${v.replace(/"/g, '""')}"`;
    const rows = entries.map((e) => [e.created_at, who(e), e.action, target(e.target), e.detail ?? ""].map(esc).join(","));
    const blob = new Blob([["time,user,action,target,detail", ...rows].join("\n")], { type: "text/csv" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "audit.csv";
    a.click();
    URL.revokeObjectURL(a.href);
  }
</script>

<section class="panel">
  <div class="container col">
    <h3>Audit log</h3>
    <div class="row" style="flex-wrap:wrap;">
      <input style="flex:1;min-width:180px;" placeholder="Search action, target or detail" bind:value={q} oninput={changed} />
      <select bind:value={range} onchange={changed}>
        {#each ranges as r}<option value={r.id}>{r.label}</option>{/each}
      </select>
      <select bind:value={action} onchange={changed} aria-label="Action">
        <option value="">All actions</option>
        {#if action && !stats?.actions.some((a) => a.key === action)}<option value={action}>{action}</option>{/if}
        {#each stats?.actions ?? [] as a}<option value={a.key}>{a.key} ({a.count})</option>{/each}
      </select>
      <select bind:value={userSel} onchange={changed} aria-label="User">
        <option value="">All users</option>
        {#if userSel && !stats?.users.some((u) => u.key === userSel)}<option value={userSel}>{userSel}</option>{/if}
        {#each stats?.users ?? [] as u}<option value={u.key}>{u.key} ({u.count})</option>{/each}
      </select>
    </div>

    {#if stats}
      <div class="card">
        <div class="row"><b class="muted">Matching events</b><span class="spacer"></span>{stats.total.toLocaleString()}</div>
        <div class="row"><b class="muted">Distinct users</b><span class="spacer"></span>{stats.users.length}</div>
        <div class="row"><b class="muted">Distinct actions</b><span class="spacer"></span>{stats.actions.length}</div>
      </div>

      {#if days.length > 1}
        <h4>Events per day</h4>
        <div class="days" role="img" aria-label="Events per day">
          {#each days as d}
            <div class="bar" title="{d.key}: {d.count}" style="height:{Math.max(2, (d.count * 100) / maxOf(days))}%"></div>
          {/each}
        </div>
        <div class="row muted small"><span>{days[0].key}</span><span class="spacer"></span><span>{days[days.length - 1].key}</span></div>
      {/if}

      <div class="cols">
        {#each [{ t: "By action", rows: stats.actions, kind: "action" as const, sel: action }, { t: "By user", rows: stats.users, kind: "user" as const, sel: userSel }] as g}
          <div class="col" style="gap:2px;">
            <h4>{g.t}</h4>
            {#each g.rows.slice(0, 12) as r}
              <button class="brk" class:on={g.sel === r.key} onclick={() => pick(g.kind, r.key)} title="Filter to {r.key}">
                <span class="fill" style="width:{(r.count * 100) / maxOf(g.rows)}%"></span>
                <span class="lbl">{r.key || "(unknown)"}</span><span class="n">{r.count}</span>
              </button>
            {/each}
          </div>
        {/each}
      </div>
    {/if}

    <div class="row">
      <h4 style="margin:0;">Events</h4><span class="spacer"></span>
      <button onclick={exportCsv} disabled={!entries.length}>Export CSV</button>
    </div>
    {#if loading && !entries.length}
      <div class="muted small">Loading…</div>
    {:else if !entries.length}
      <div class="muted small">No events match.</div>
    {:else}
      <div class="tbl">
        {#each entries as e (e.user_id + ":" + e.id)}
          <div class="ev">
            <span class="muted small">{when(e.created_at)}</span>
            <span class="chip">{e.action}</span>
            <span class="tgt" title={target(e.target) + (e.detail ? "\n" + e.detail : "")}>{target(e.target)}</span>
            <span class="muted small">{who(e)}</span>
          </div>
        {/each}
      </div>
      {#if stats && entries.length < stats.total}
        <div><button onclick={() => { limit += 200; load(); }}>Show more ({entries.length} of {stats.total})</button></div>
      {/if}
    {/if}
    <div class="muted small">Entries are kept in each user's own data, so deleting a user also removes their audit history. Days follow your local time.</div>
  </div>
</section>

<style>
  h4 { margin: 8px 0 2px; font-size: 13px; }
  .days { display: flex; align-items: flex-end; gap: 2px; height: 70px; }
  .bar { flex: 1; min-width: 2px; background: var(--accent-dim); border-radius: 2px 2px 0 0; }
  .cols { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
  .brk { position: relative; display: flex; justify-content: space-between; gap: 8px; padding: 3px 8px; min-height: 26px;
    background: transparent; border: 1px solid transparent; box-shadow: none; overflow: hidden; text-align: left; }
  .brk.on { border-color: var(--accent-dim); }
  .fill { position: absolute; inset: 0 auto 0 0; background: var(--accent-soft); z-index: 0; }
  .lbl, .n { position: relative; z-index: 1; font-size: 12px; }
  .lbl { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tbl { display: flex; flex-direction: column; }
  .ev { display: grid; grid-template-columns: 150px 150px 1fr auto; gap: 8px; align-items: center; padding: 4px 0;
    border-bottom: 1px solid var(--border-soft); }
  .chip { justify-self: start; }
  .tgt { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
</style>
