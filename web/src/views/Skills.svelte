<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { user, toast } from "../lib/stores";
  import { isElevated } from "../lib/types";
  import type { SkillInfo, SkillFiles } from "../lib/types";
  import SkillEditor from "../lib/SkillEditor.svelte";

  let skills = $state<SkillInfo[]>([]);
  let open = $state<SkillFiles | null>(null);
  let editing = $state<{ name: string; path: string } | null>(null);

  // New-skill form.
  let newName = $state("");
  let newDesc = $state("");
  let newScope = $state("");

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
  function edit(name: string, path: string) {
    open = null;
    editing = { name, path };
  }
  async function onEditorClosed() {
    editing = null;
    await load();
  }
  async function create() {
    if (!newName.trim()) return;
    try {
      await api.createSkill(newName.trim(), newDesc.trim(), newScope);
      toast(`created ${newName}`);
      const name = newName.trim();
      newName = "";
      newDesc = "";
      await load();
      edit(name, "SKILL.md");
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
  async function reset(name: string, scope: string) {
    try {
      await api.resetSkill(name, scope);
      toast(`deleted ${name} (${scope || "default"})`);
      await load();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
</script>

<section class="panel">
  <div class="container col">
    <h3>Skills</h3>

    <div class="card col">
      <strong>New skill</strong>
      <div class="row" style="gap:6px; flex-wrap:wrap;">
        <input placeholder="name" bind:value={newName} />
        <input placeholder="description" bind:value={newDesc} style="flex:1; min-width:160px;" />
        <select bind:value={newScope}>
          <option value="">default</option>
          <option value="user">user</option>
          <option value="project">project</option>
          {#if isElevated($user?.role)}<option value="shared">shared</option>{/if}
        </select>
        <button class="primary" onclick={create}>Create</button>
      </div>
    </div>

    <div class="list">
      {#each skills as s}
        <div class="card col">
          <div class="row">
            <strong>{s.name}</strong><span class="pill">{s.source}</span>
            {#if s.also_in?.length}
              <span class="pill warn" title="A copy in {s.also_in.join(', ')} is shadowed by the {s.source} version — edits there are invisible.">shadows {s.also_in.join(", ")}</span>
            {/if}
            <span class="spacer"></span>
            <button class="small" onclick={() => edit(s.name, "SKILL.md")}>Edit</button>
            <button class="small" onclick={() => viewSkill(s.name)}>Files</button>
          </div>
          <div class="muted small">{s.description}</div>
          <div class="row small" style="gap:6px;">
            <button onclick={() => reset(s.name, s.source)}>Delete ({s.source})</button>
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
    <header>
      <strong>{open.name}</strong><span class="pill">{open.scope}</span>
      <span class="spacer"></span>
      <button class="ghost" onclick={() => (open = null)}>Close</button>
    </header>
    <div class="body col">
      {#each Object.entries(open.files) as [path, content]}
        <div class="row">
          <div class="muted small mono">=== {path} ===</div>
          <span class="spacer"></span>
          <button class="small" onclick={() => edit(open!.name, path)}>Edit</button>
        </div>
        <pre class="mono">{content}</pre>
      {/each}
    </div>
  </aside>
{/if}

{#if editing}
  <SkillEditor name={editing.name} path={editing.path} onClose={onEditorClosed} />
{/if}
