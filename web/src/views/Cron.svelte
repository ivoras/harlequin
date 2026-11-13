<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast } from "../lib/stores";
  import type { CronJob, CreateCronJobRequest, SkillInfo, Hat } from "../lib/types";
  import { PRESETS, WEEKDAYS, buildSpec, parseSpec, defaultSched, describe, type Sched } from "../lib/cronspec";

  let jobs = $state<CronJob[]>([]);
  let showAdd = $state(false);
  let editingID = $state<number | null>(null); // non-null: the form saves an existing job
  const blank = (): CreateCronJobRequest => ({ name: "", spec: buildSpec(defaultSched()), kind: "skill", target: "", prompt: "", hat: "", input: "", notify_channel: "inapp" });
  let f = $state<CreateCronJobRequest>(blank());

  // Schedule: pick a common period, or tick "Custom" to type a raw cron spec.
  let sched = $state<Sched>(defaultSched());
  let custom = $state(false);
  const cur = $derived(PRESETS.find((p) => p.id === sched.preset)!);
  $effect(() => {
    if (!custom) f.spec = buildSpec(sched);
  });
  function toggleCustom() {
    custom = !custom;
    if (!custom) {
      // Back to presets: keep the typed spec if it is one, else the last preset.
      const p = parseSpec(f.spec);
      if (p) sched = p;
    }
  }

  // Skills for the picker (skill jobs); the job's current target stays selectable
  // even if the skill is gone, so editing never silently changes it.
  let skills = $state<SkillInfo[]>([]);
  const skillOptions = $derived(
    f.target && !skills.some((k) => k.name === f.target) ? [{ name: f.target, description: "(not found)" } as SkillInfo, ...skills] : skills,
  );
  // Hats for the picker; a job's current hat stays selectable even if it is gone.
  let hats = $state<Hat[]>([]);
  const hatOptions = $derived(f.hat && !hats.some((h) => h.name === f.hat) ? [{ name: f.hat, description: "(not found)" } as Hat, ...hats] : hats);
  const short = (t: string) => (t.length > 70 ? t.slice(0, 69) + "…" : t);
  function resetForm() {
    f = blank();
    sched = defaultSched();
    custom = false;
  }

  async function load() {
    try {
      jobs = await api.listCron();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  onMount(() => {
    load();
    api.listSkills().then((l) => (skills = l)).catch(() => {});
    api.listHats().then((l) => (hats = l)).catch(() => {});
  });
  async function add() {
    const wasEdit = editingID !== null;
    try {
      if (editingID !== null) {
        await api.updateCron(editingID, { ...f });
      } else {
        await api.createCron({ ...f });
      }
      showAdd = false;
      editingID = null;
      resetForm();
      await load();
      toast(wasEdit ? "saved" : "scheduled");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  function edit(j: CronJob) {
    editingID = j.id;
    f = { name: j.name, spec: j.spec, kind: j.kind, target: j.target, prompt: j.prompt ?? "", hat: j.hat ?? "", input: j.input ?? "", notify_channel: j.notify_channel || "inapp" };
    const p = parseSpec(j.spec);
    custom = !p;
    sched = p ?? defaultSched();
    showAdd = true;
  }
  async function toggle(j: CronJob) {
    try {
      await api.updateCron(j.id, { enabled: !j.enabled });
      await load();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function run(j: CronJob) {
    try {
      await api.runCron(j.id);
      toast(`running "${j.name}"…`);
      // The run is async on the server; give it a moment, then refresh so the
      // row's last-run status/output updates. (A job whose output is unchanged
      // won't raise a notification by design — the refreshed row is the signal.)
      setTimeout(load, 3000);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function del(j: CronJob) {
    if (!confirm(`Delete cron job "${j.name}"?`)) return;
    try {
      await api.deleteCron(j.id);
      jobs = jobs.filter((x) => x.id !== j.id);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
</script>

<section class="panel">
  <div class="container col">
    <div class="row"><h3>Cron</h3><span class="spacer"></span><button class="primary small" onclick={() => { showAdd = editingID !== null ? true : !showAdd; editingID = null; resetForm(); }}>+ Add</button></div>
    {#if showAdd}
      <div class="card col">
        <input placeholder="name" bind:value={f.name} />
        <div class="col" style="gap:6px;">
          {#if custom}
            <input placeholder="cron spec — e.g. @every 12h  or  0 0,12 * * *" bind:value={f.spec} />
          {:else}
            <div class="row" style="gap:6px; flex-wrap:wrap;">
              <select bind:value={sched.preset} aria-label="Schedule">
                {#each PRESETS as p (p.id)}<option value={p.id}>{p.label}</option>{/each}
              </select>
              {#if cur.dow}
                <select bind:value={sched.dow} aria-label="Day of week">
                  {#each WEEKDAYS as d, i}<option value={i}>{d}</option>{/each}
                </select>
              {/if}
              {#if cur.time}<input type="time" bind:value={sched.time} aria-label="Time of day" />{/if}
            </div>
          {/if}
          <label class="muted small row" style="gap:6px;"><input type="checkbox" checked={custom} onchange={toggleCustom} /> Custom (cron expression)</label>
          <div class="muted small">Times are server time · spec <code>{f.spec}</code></div>
        </div>
        <select bind:value={f.kind} onchange={() => { f.target = ""; f.hat = ""; }}>
          <option value="skill">skill — run an agent turn</option>
          <option value="js">js — run a script, no AI</option>
        </select>
        {#if f.kind === "js"}
          <textarea class="mono" rows="6" placeholder="target: skill://… / storage://… or inline JS" bind:value={f.target}></textarea>
        {:else}
          <select bind:value={f.target} aria-label="Skill">
            <option value="" disabled>Choose a skill…</option>
            {#each skillOptions as k (k.name)}<option value={k.name}>{k.name}{k.description ? " — " + short(k.description) : ""}</option>{/each}
          </select>
        {/if}
        {#if f.kind === "skill"}<input placeholder="prompt for the agent" bind:value={f.prompt} />{/if}
        {#if f.kind === "skill" && hatOptions.length}
          <select bind:value={f.hat} aria-label="Hat">
            <option value="">No hat</option>
            {#each hatOptions as h (h.name)}<option value={h.name}>🎩 {h.name}{h.description ? " — " + short(h.description) : ""}</option>{/each}
          </select>
        {/if}
        <input placeholder={'input JSON — e.g. {"name":"fzoeu"}'} bind:value={f.input} />
        <label class="muted small">Notify via
          <select bind:value={f.notify_channel}>
            <option value="inapp">in-app</option>
            <option value="email">email</option>
            <option value="telegram">telegram</option>
          </select>
        </label>
        <button class="primary" onclick={add} disabled={!f.name.trim() || !f.spec.trim() || !f.target.trim()}>{editingID !== null ? "Save" : "Create"}</button>
      </div>
    {/if}
    <div class="list">
      {#each jobs as j}
        <div class="card col">
          <div class="row">
            <strong>{j.name}</strong><span class="pill">{j.kind}</span><span class="pill" title={j.spec}>{describe(j.spec)}</span>
            {#if j.hat}<span class="pill" title="The run wears this hat">🎩 {j.hat}</span>{/if}
            {#if j.notify_channel && j.notify_channel !== "inapp"}<span class="pill">→ {j.notify_channel}</span>{/if}
            <span class="spacer"></span>{#if !j.enabled}<span class="muted small">disabled</span>{/if}
          </div>
          <div class="muted small wrap">{j.target}</div>
          {#if j.last_status}<div class="muted small wrap">last{#if j.last_run_at} <span class="mono">({new Date(j.last_run_at).toLocaleString()})</span>{/if}: {j.last_status} — {j.last_output}</div>{/if}
          <div class="row small" style="gap:6px;">
            <button onclick={() => run(j)}>Run now</button>
            <button onclick={() => toggle(j)}>{j.enabled ? "Disable" : "Enable"}</button>
            <button onclick={() => edit(j)}>Edit</button>
            <span class="spacer"></span><button class="ghost danger" onclick={() => del(j)}>Delete</button>
          </div>
        </div>
      {/each}
      {#if jobs.length === 0}<div class="muted small">No scheduled jobs.</div>{/if}
    </div>
  </div>
</section>
