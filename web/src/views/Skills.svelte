<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { user, toast } from "../lib/stores";
  import { isElevated } from "../lib/types";
  import type { SkillInfo, SkillFiles } from "../lib/types";

  let skills = $state<SkillInfo[]>([]);
  let open = $state<SkillFiles | null>(null);

  async function load() {
    try {
      skills = await api.listSkills();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  onMount(load);
  async function viewSkill(name: string) {
    try {
      open = await api.getSkill(name);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function publish(name: string) {
    try {
      await api.publishSkill(name);
      toast(`published ${name}`);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function reset(name: string) {
    try {
      await api.resetSkill(name);
      toast(`reset ${name}`);
      await load();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
</script>

<section class="panel">
  <div class="container col">
    <h3>Skills</h3>
    <div class="list">
      {#each skills as s}
        <div class="card col">
          <div class="row">
            <strong>{s.name}</strong><span class="pill">{s.source}</span>
            <span class="spacer"></span><button class="small" onclick={() => viewSkill(s.name)}>View</button>
          </div>
          <div class="muted small">{s.description}</div>
          <div class="row small" style="gap:6px;">
            {#if s.source === "override"}<button onclick={() => reset(s.name)}>Reset override</button>{/if}
            {#if isElevated($user?.role)}<button onclick={() => publish(s.name)}>Publish (org)</button>{/if}
          </div>
        </div>
      {/each}
      {#if skills.length === 0}<div class="muted small">No skills.</div>{/if}
    </div>
  </div>
</section>

{#if open}
  <div class="scrim" role="presentation" onclick={() => (open = null)}></div>
  <aside class="sheet bottom">
    <header><strong>{open.name}</strong><span class="spacer"></span><button class="ghost" onclick={() => (open = null)}>Close</button></header>
    <div class="body col">
      {#each Object.entries(open.files) as [path, content]}
        <div class="muted small mono">=== {path} ===</div>
        <pre class="mono">{content}</pre>
      {/each}
    </div>
  </aside>
{/if}
