<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { session, wornHat, user, toast } from "../lib/stores";
  import { isElevated } from "../lib/types";
  import type { Hat, SkillInfo } from "../lib/types";
  import SkillEditor from "../lib/SkillEditor.svelte";

  let hats = $state<Hat[]>([]);
  let skills = $state<SkillInfo[]>([]);
  let editing = $state<{ name: string; path: string; initial?: string } | null>(null);
  let filesOf = $state<{ name: string; files: string[] } | null>(null);
  let addSel = $state<Record<string, string>>({}); // hat -> skill picked in its add-select

  // New-hat form (admins).
  let newName = $state("");
  let newDesc = $state("");

  let admin = $derived(isElevated($user?.role));

  async function load() {
    try {
      hats = await api.listHats();
      if (admin) skills = await api.listSkills();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  onMount(load);

  async function wear(h: Hat) {
    if (!$session.id) return toast("no active session", "error");
    try {
      await api.setSessionHat($session.id, h.name);
      wornHat.set(h.name);
      toast(`wearing the ${h.name} hat`);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function off() {
    if (!$session.id) return;
    try {
      await api.setSessionHat($session.id, "");
      wornHat.set("");
      toast("hat off");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function create() {
    if (!newName.trim()) return;
    try {
      await api.createHat(newName.trim(), newDesc.trim());
      toast(`created hat ${newName}`);
      const name = newName.trim();
      newName = "";
      newDesc = "";
      await load();
      editing = { name, path: "system_prompt.md" };
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function del(h: Hat) {
    if (!confirm(`Delete the "${h.name}" hat?`)) return;
    try {
      await api.deleteHat(h.name);
      toast(`deleted hat ${h.name}`);
      await load();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function addSkill(h: Hat) {
    const skill = addSel[h.name];
    if (!skill) return;
    try {
      await api.addHatSkill(h.name, skill);
      toast(`copied ${skill} into ${h.name}`);
      addSel[h.name] = "";
      await load();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function rmSkill(h: Hat, skill: string) {
    try {
      await api.removeHatSkill(h.name, skill);
      toast(`removed ${skill} overlay from ${h.name}`);
      await load();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function showFiles(h: Hat) {
    try {
      const f = await api.getHatFiles(h.name);
      filesOf = { name: h.name, files: Object.keys(f.files).sort() };
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  function editFile(name: string, path: string) {
    filesOf = null;
    editing = { name, path };
  }
  // Open the system-prompt editor; when the hat has no custom prompt yet, seed
  // the body with the default system prompt template so specialising starts
  // from the real default.
  async function editPrompt(h: Hat) {
    if (h.has_custom_prompt) {
      editFile(h.name, "system_prompt.md");
      return;
    }
    try {
      const [file, tpl] = await Promise.all([
        api.getHatFile(h.name, "system_prompt.md"),
        api.getSystemPromptTemplate(),
      ]);
      const seeded = file.content.replace(/\s+$/, "") + "\n" + tpl.content;
      editing = { name: h.name, path: "system_prompt.md", initial: seeded };
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function togglePrompt(h: Hat) {
    try {
      await api.setHatPromptEnabled(h.name, !!h.prompt_disabled);
      toast(h.prompt_disabled ? "custom prompt enabled" : "custom prompt disabled (content kept)");
      await load();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function onEditorClosed() {
    editing = null;
    await load();
  }
</script>

<section class="panel">
  <div class="container col">
    <div class="row"><h3>Hats</h3><span class="spacer"></span>
      <button class="small" onclick={off} disabled={!$wornHat}>Take off{$wornHat ? ` (${$wornHat})` : ""}</button></div>
    <div class="muted small">
      A hat is a set of specialised skills that <b>overlay</b> normal resolution:
      while worn, its skill copies take precedence over the project/shared/user
      versions, and its optional visibility list restricts which skills the agent
      sees. It can also replace the system prompt.
    </div>

    {#if admin}
      <div class="card col">
        <strong>New hat</strong>
        <div class="row" style="gap:6px; flex-wrap:wrap;">
          <input placeholder="name" bind:value={newName} style="max-width:180px;" />
          <input placeholder="description" bind:value={newDesc} style="flex:1; min-width:160px;" />
          <button class="primary" onclick={create}>Create</button>
        </div>
      </div>
    {/if}

    <div class="list">
      {#each hats as h}
        <div class="card col">
          <div class="row">
            <strong>{h.name}</strong>
            {#if $wornHat === h.name}<span class="pill" style="color: var(--accent);">worn</span>{/if}
            <span class="spacer"></span>
            {#if $wornHat === h.name}
              <button class="small" onclick={off}>Take off</button>
            {:else}
              <button class="primary small" onclick={() => wear(h)}>Wear</button>
            {/if}
          </div>
          <div class="muted small">{h.description}</div>
          <div class="muted small">
            prompt:
            {#if h.has_custom_prompt && h.prompt_disabled}<span class="warn">custom, disabled — default in use</span>
            {:else if h.has_custom_prompt}custom (replaces the default)
            {:else}default{/if}
          </div>
          {#if h.skills && h.skills.length}
            <div class="muted small">visible skills: {h.skills.join(", ")}</div>
          {/if}
          {#if h.overlay_skills?.length}
            <div class="row small" style="flex-wrap:wrap; gap:4px;">
              <span class="muted">overlays:</span>
              {#each h.overlay_skills as sk}
                <span class="pill">
                  {sk}
                  {#if admin}
                    <button class="ovbtn" title="edit overlay" onclick={() => editFile(h.name, `skills/${sk}/SKILL.md`)}>✎</button>
                    <button class="ovbtn" title="remove overlay" onclick={() => rmSkill(h, sk)}>✕</button>
                  {/if}
                </span>
              {/each}
            </div>
          {/if}
          {#if admin}
            <div class="row small" style="gap:6px; flex-wrap:wrap;">
              <button class="small" onclick={() => editPrompt(h)} title="The hat's system_prompt.md: frontmatter (description, visible skills) + optional body that replaces the default system prompt while worn. Creating one starts from a copy of the default.">{h.has_custom_prompt ? "Edit system prompt" : "Create system prompt"}</button>
              {#if h.has_custom_prompt}
                <button class="small" onclick={() => togglePrompt(h)}>{h.prompt_disabled ? "Enable prompt" : "Use default prompt"}</button>
              {/if}
              <button class="small" onclick={() => showFiles(h)}>Files</button>
              <select bind:value={addSel[h.name]}>
                <option value="">add skill overlay…</option>
                {#each skills.filter((s) => !h.overlay_skills?.includes(s.name)) as s}
                  <option value={s.name}>{s.name}</option>
                {/each}
              </select>
              <button class="small" onclick={() => addSkill(h)} disabled={!addSel[h.name]}>Add</button>
              <span class="spacer"></span>
              <button class="ghost danger small" onclick={() => del(h)}>Delete hat</button>
            </div>
          {/if}
        </div>
      {/each}
      {#if hats.length === 0}<div class="muted small">No hats defined.</div>{/if}
    </div>
  </div>
</section>

{#if filesOf}
  <div class="scrim" role="presentation" onclick={() => (filesOf = null)}></div>
  <aside class="sheet bottom">
    <header>
      <strong>hat://{filesOf.name}</strong>
      <span class="spacer"></span>
      <button class="ghost" onclick={() => (filesOf = null)}>Close</button>
    </header>
    <div class="body col">
      {#each filesOf.files as path}
        <div class="row">
          <span class="mono small">{path}</span>
          <span class="spacer"></span>
          {#if admin}<button class="small" onclick={() => editFile(filesOf!.name, path)}>Edit</button>{/if}
        </div>
      {/each}
    </div>
  </aside>
{/if}

{#if editing}
  <SkillEditor name={editing.name} path={editing.path} initial={editing.initial} hat onClose={onEditorClosed} />
{/if}

<style>
  .ovbtn {
    background: none;
    border: none;
    box-shadow: none;
    padding: 0 2px;
    min-height: 0;
    font-size: 11px;
    color: var(--muted);
  }
  .ovbtn:hover { color: var(--accent); }
</style>
