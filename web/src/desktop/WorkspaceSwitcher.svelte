<script lang="ts">
  import { api } from "../lib/api";
  import { activeProject, view, toast } from "../lib/stores";
  import { switchToProject, leaveActiveProject, projectBylines } from "../lib/project";
  import type { Project, ProjectInvite } from "../lib/types";
  import Icon from "./Icon.svelte";
  import { ui } from "./state.svelte";

  // The sidebar's workspace picker: personal vs one project. Switching runs the
  // same flow as the mobile project sheet (lib/project).
  let open = $state(false);
  let newName = $state("");
  const bylines = $derived(projectBylines(ui.projects));

  function toggle() {
    open = !open;
    if (open) ui.loadProjects();
  }
  async function pick(p: Project | null) {
    open = false;
    if (p === null) {
      if ($activeProject) await leaveActiveProject();
      return;
    }
    if (p.id !== $activeProject?.id) await switchToProject(p);
  }
  async function create() {
    const name = newName.trim();
    if (!name) return;
    try {
      const p = await api.createProject(name);
      newName = "";
      await ui.loadProjects();
      await pick(p);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function accept(inv: ProjectInvite) {
    try {
      await api.acceptInvite(inv.id);
      await ui.loadProjects();
      const p = ui.projects.find((x) => x.id === inv.project_id);
      if (p) await pick(p);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
</script>

<div class="ws">
  <button class="ws-btn" onclick={toggle} aria-haspopup="menu" aria-expanded={open}
    title="Switch workspace">
    <span class="ws-ic" class:proj={!!$activeProject}>
      <Icon name={$activeProject ? "folder" : "user"} size={14} />
    </span>
    <span class="ws-name">
      <span class="ws-kind">{$activeProject ? "Project" : "Workspace"}</span>
      <span class="ellipsize">{$activeProject ? $activeProject.name : "Personal"}</span>
    </span>
    {#if ui.invites.length}<span class="badge" title="Pending invitations">{ui.invites.length}</span>{/if}
    <Icon name="chevronDown" size={14} />
  </button>

  {#if open}
    <div class="menu-click-away" role="presentation" onclick={() => (open = false)}></div>
    <div class="popover ws-pop" role="menu">
      <button class="menu-item" onclick={() => pick(null)}>
        <Icon name="user" /><span class="grow">Personal</span>
        {#if !$activeProject}<span class="check"><Icon name="check" /></span>{/if}
      </button>
      {#if ui.projects.length}<div class="menu-label">Projects</div>{/if}
      {#each ui.projects as p (p.id)}
        <button class="menu-item" onclick={() => pick(p)}>
          <Icon name="folder" />
          <span class="grow">{p.name}{#if bylines.get(p.id)}<span class="faint small"> · {bylines.get(p.id)}</span>{/if}</span>
          {#if $activeProject?.id === p.id}<span class="check"><Icon name="check" /></span>{/if}
        </button>
      {/each}
      {#if ui.invites.length}
        <div class="menu-label">Invitations</div>
        {#each ui.invites as inv (inv.id)}
          <div class="menu-item inv">
            <span class="grow">{inv.project_name}<span class="faint small"> · from {inv.invited_by}</span></span>
            <button class="primary small" onclick={() => accept(inv)}>Accept</button>
          </div>
        {/each}
      {/if}
      <div class="menu-sep"></div>
      <div class="row newproj">
        <input placeholder="New project name" bind:value={newName} onkeydown={(e) => e.key === "Enter" && create()} />
        <button class="small" onclick={create} disabled={!newName.trim()}>Create</button>
      </div>
      <button class="menu-item" onclick={() => { open = false; view.set("projects"); }}>
        <Icon name="users" /><span class="grow">Manage projects…</span>
      </button>
    </div>
  {/if}
</div>

<style>
  .ws { position: relative; padding: 0 10px; }
  .ws-btn { width: 100%; justify-content: flex-start; gap: 9px; padding: 6px 8px; min-height: 44px;
    background: var(--surface); text-align: left; }
  .ws-ic { width: 26px; height: 26px; border-radius: 7px; display: grid; place-items: center; flex: none;
    background: color-mix(in srgb, var(--personal) 14%, transparent); color: var(--personal); }
  .ws-ic.proj { background: color-mix(in srgb, var(--purple) 16%, transparent); color: var(--purple); }
  .ws-name { flex: 1; min-width: 0; display: flex; flex-direction: column; line-height: 1.25; }
  .ws-kind { font-size: 10.5px; color: var(--faint); text-transform: uppercase; letter-spacing: 0.05em; }
  .ws-name .ellipsize { font-weight: 600; }
  .badge { background: var(--accent-dim); color: var(--on-accent); border-radius: 999px; font-size: 11px;
    padding: 0 6px; font-weight: 700; }
  .ws-pop { left: 10px; right: 10px; top: calc(100% + 4px); }
  .inv { cursor: default; }
  .newproj { padding: 4px 4px 6px; }
  .newproj input { padding: 4px 8px; }
</style>
