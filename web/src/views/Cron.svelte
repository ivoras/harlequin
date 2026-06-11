<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast } from "../lib/stores";
  import type { CronJob, CreateCronJobRequest } from "../lib/types";

  let jobs = $state<CronJob[]>([]);
  let showAdd = $state(false);
  const blank = (): CreateCronJobRequest => ({ name: "", spec: "@every 12h", kind: "js", target: "", prompt: "", input: "", notify_channel: "inapp" });
  let f = $state<CreateCronJobRequest>(blank());

  async function load() {
    try {
      jobs = await api.listCron();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  onMount(load);
  async function add() {
    try {
      await api.createCron({ ...f });
      showAdd = false;
      f = blank();
      await load();
      toast("scheduled");
    } catch (e) {
      toast((e as Error).message, "error");
    }
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
    <div class="row"><h3>Cron</h3><span class="spacer"></span><button class="primary small" onclick={() => (showAdd = !showAdd)}>+ Add</button></div>
    {#if showAdd}
      <div class="card col">
        <input placeholder="name" bind:value={f.name} />
        <input placeholder="spec — e.g. @every 12h  or  0 0,12 * * *" bind:value={f.spec} />
        <select bind:value={f.kind}>
          <option value="js">js — run a script, no AI</option>
          <option value="skill">skill — run an agent turn</option>
        </select>
        <input placeholder={f.kind === "js" ? "target: skill://… / storage://… or inline JS" : "target: skill name (optional)"} bind:value={f.target} />
        {#if f.kind === "skill"}<input placeholder="prompt for the agent" bind:value={f.prompt} />{/if}
        <input placeholder={'input JSON — e.g. {"name":"fzoeu"}'} bind:value={f.input} />
        <label class="muted small">Notify via
          <select bind:value={f.notify_channel}>
            <option value="inapp">in-app</option>
            <option value="email">email</option>
            <option value="telegram">telegram</option>
          </select>
        </label>
        <button class="primary" onclick={add} disabled={!f.name.trim() || !f.spec.trim() || !f.target.trim()}>Create</button>
      </div>
    {/if}
    <div class="list">
      {#each jobs as j}
        <div class="card col">
          <div class="row">
            <strong>{j.name}</strong><span class="pill">{j.kind}</span><span class="pill">{j.spec}</span>
            {#if j.notify_channel && j.notify_channel !== "inapp"}<span class="pill">→ {j.notify_channel}</span>{/if}
            <span class="spacer"></span>{#if !j.enabled}<span class="muted small">disabled</span>{/if}
          </div>
          <div class="muted small wrap">{j.target}</div>
          {#if j.last_status}<div class="muted small wrap">last{#if j.last_run_at} <span class="mono">({new Date(j.last_run_at).toLocaleString()})</span>{/if}: {j.last_status} — {j.last_output}</div>{/if}
          <div class="row small" style="gap:6px;">
            <button onclick={() => run(j)}>Run now</button>
            <button onclick={() => toggle(j)}>{j.enabled ? "Disable" : "Enable"}</button>
            <span class="spacer"></span><button class="ghost danger" onclick={() => del(j)}>Delete</button>
          </div>
        </div>
      {/each}
      {#if jobs.length === 0}<div class="muted small">No scheduled jobs.</div>{/if}
    </div>
  </div>
</section>
