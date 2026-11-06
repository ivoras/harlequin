<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { activeProject, session, toast } from "../lib/stores";
  import { switchToProject, leaveActiveProject, projectBylines, canDeleteProject, deleteProjectFlow } from "../lib/project";
  import type { Document, Project, ProjectInvite, ProjectMember } from "../lib/types";
  import Icon from "./Icon.svelte";
  import TopBar from "./TopBar.svelte";
  import FileTable from "./FileTable.svelte";
  import { ui } from "./state.svelte";

  // Browse projects (a local pick — browsing does not switch the chat
  // workspace), manage members and the picked project's files, and switch.
  let selected = $state(0);
  let members = $state<ProjectMember[]>([]);
  let docs = $state<Document[]>([]);
  let directory = $state<{ id: number; email: string }[]>([]);
  let inviteEmail = $state("");
  let newName = $state("");
  const bylines = $derived(projectBylines(ui.projects));
  const sel = $derived(ui.projects.find((p) => p.id === selected) ?? null);
  const suggestions = $derived(directory.filter((u) => !members.some((m) => m.user_id === u.id)));

  onMount(() => {
    ui.loadProjects().then(() => {
      if (!ui.projects.some((p) => p.id === selected)) selected = $activeProject?.id ?? ui.projects[0]?.id ?? 0;
    });
    api.userDirectory().then((d) => (directory = d)).catch(() => {});
  });

  // Load the picked project's members and project-scope files.
  $effect(() => {
    const id = selected;
    void ui.docsVersion;
    members = [];
    docs = [];
    if (!id) return;
    api.getProject(id).then((p) => selected === id && (members = p.members ?? [])).catch(() => {});
    api
      .listDocuments(id)
      .then((all) => selected === id && (docs = all.filter((d) => d.scope === "project")))
      .catch((e) => toast((e as Error).message, "error"));
  });

  // Drops on this page land in the picked project.
  $effect(() => {
    const p = sel;
    ui.dropTarget = p ? { scope: "project", projectID: p.id, label: `project “${p.name}”` } : null;
    return () => (ui.dropTarget = null);
  });

  async function create() {
    const name = newName.trim();
    if (!name) return;
    try {
      const p = await api.createProject(name);
      newName = "";
      await ui.loadProjects();
      selected = p.id;
      toast(`created ${p.name}`);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function accept(inv: ProjectInvite) {
    try {
      await api.acceptInvite(inv.id);
      await ui.loadProjects();
      selected = inv.project_id;
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function invite() {
    const email = inviteEmail.trim();
    if (!email || !sel) return;
    try {
      await api.inviteToProject(sel.id, email);
      inviteEmail = "";
      toast(`invited ${email} — they'll get a notification to accept`);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function assignCurrent() {
    const p = $activeProject;
    if (!p || !$session.id) return;
    if (!confirm(`Move the current session into "${p.name}"? Every member will see it.`)) return;
    try {
      const { session_id } = await api.assignSession(p.id, $session.id);
      session.set({ id: session_id, title: $session.title });
      toast("session assigned to " + p.name);
      ui.loadSessions();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function depart(p: Project) {
    if (!confirm(`Depart "${p.name}"? This removes your membership.`)) return;
    try {
      await api.departProject(p.id);
      toast("departed " + p.name);
      if ($activeProject?.id === p.id) await leaveActiveProject();
      await ui.loadProjects();
      selected = ui.projects[0]?.id ?? 0;
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function del(p: Project) {
    if (!(await deleteProjectFlow(p))) return;
    await ui.loadProjects();
    selected = ui.projects[0]?.id ?? 0;
  }
  async function delDoc(d: Document) {
    if (!sel || !confirm(`Delete "${d.title}" from ${sel.name}? This permanently removes the file, its extracted Markdown text and its search entries for every member. It cannot be undone.`)) return;
    try {
      await api.deleteDocument(d.id, "project", sel.id);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      ui.docsChanged();
    }
  }
  function pick() {
    if (!sel) return;
    const target = { scope: "project" as const, projectID: sel.id, label: `project “${sel.name}”` };
    const inp = document.createElement("input");
    inp.type = "file";
    inp.multiple = true;
    inp.onchange = () => inp.files && ui.upload([...inp.files], target);
    inp.click();
  }
</script>

<TopBar label="Projects" />

<div class="split">
  <div class="plist">
    {#if ui.invites.length}
      <div class="menu-label">Invitations</div>
      {#each ui.invites as inv (inv.id)}
        <div class="invite">
          <div class="col" style="gap:0; min-width:0; flex:1;">
            <span class="ellipsize">{inv.project_name}</span>
            <span class="faint small ellipsize">from {inv.invited_by}</span>
          </div>
          <button class="primary small" onclick={() => accept(inv)}>Accept</button>
        </div>
      {/each}
    {/if}
    <div class="menu-label">Your projects</div>
    {#each ui.projects as p (p.id)}
      <button class="pitem" class:sel={p.id === selected} onclick={() => (selected = p.id)}>
        <Icon name="folder" size={15} />
        <span class="col" style="gap:0; min-width:0; flex:1; text-align:left;">
          <span class="ellipsize">{p.name}</span>
          {#if bylines.get(p.id)}<span class="faint small ellipsize">{bylines.get(p.id)}</span>{/if}
        </span>
        {#if $activeProject?.id === p.id}<span class="pill scope-project">active</span>{/if}
      </button>
    {/each}
    {#if ui.projects.length === 0}<div class="faint small" style="padding: 4px 8px;">No projects yet.</div>{/if}
    <div class="newp">
      <input placeholder="New project name" bind:value={newName} onkeydown={(e) => e.key === "Enter" && create()} />
      <button class="small" onclick={create} disabled={!newName.trim()}><Icon name="plus" size={13} /> Create</button>
    </div>
  </div>

  <div class="detail">
    {#if sel}
      <div class="container col">
        <div class="row dhead">
          <div class="col" style="gap:0; min-width:0;">
            <h2 class="ellipsize">{sel.name}</h2>
            <span class="faint small">created {new Date(sel.created_at).toLocaleDateString()}{sel.created_by_email ? ` by ${sel.created_by_email}` : ""}</span>
          </div>
          <span class="spacer"></span>
          {#if $activeProject?.id === sel.id}
            <button onclick={leaveActiveProject} title="Back to your personal workspace">Leave workspace</button>
          {:else}
            <button class="primary" onclick={() => switchToProject(sel)}>Open workspace</button>
          {/if}
        </div>

        {#if $activeProject?.id === sel.id}
          <div class="card row">
            <Icon name="message" />
            <span class="muted small" style="flex:1;">Move the session you're chatting in into this project so every member can see and continue it.</span>
            <button class="small" onclick={assignCurrent}>Assign current session</button>
          </div>
        {/if}

        <section>
          <h3>Members <span class="faint">{members.length || ""}</span></h3>
          <div class="members">
            {#each members as m (m.user_id)}
              <div class="member"><span class="avatar">{m.email.slice(0, 1).toUpperCase()}</span><span class="ellipsize">{m.email}</span>
                <span class="faint small">joined {new Date(m.joined_at).toLocaleDateString()}</span></div>
            {/each}
          </div>
          <div class="row" style="max-width: 480px; margin-top: 8px;">
            <input placeholder="Invite by email" list="d-invite-directory" bind:value={inviteEmail} onkeydown={(e) => e.key === "Enter" && invite()} />
            <datalist id="d-invite-directory">{#each suggestions as u (u.id)}<option value={u.email}></option>{/each}</datalist>
            <button onclick={invite} disabled={!inviteEmail.trim()}>Invite</button>
          </div>
        </section>

        <section>
          <div class="row">
            <h3>Files <span class="faint">{docs.length || ""}</span></h3>
            <span class="spacer"></span>
            <button class="small" onclick={pick}><Icon name="upload" size={13} /> Upload</button>
          </div>
          {#if docs.length}
            <FileTable {docs} projectID={sel.id} projectName={sel.name} showScope={false} canDelete={() => true} onDelete={delDoc} />
          {:else}
            <div class="faint small">No files yet — drop files on this page to add them to {sel.name}.</div>
          {/if}
        </section>

        <section class="danger-zone">
          <h3>Membership</h3>
          <div class="row">
            <button class="danger" onclick={() => depart(sel)}>Depart project</button>
            {#if canDeleteProject(sel)}<button class="danger" onclick={() => del(sel)}><Icon name="trash" size={13} /> Delete project…</button>{/if}
          </div>
        </section>
      </div>
    {:else}
      <div class="empty muted">
        <Icon name="users" size={32} />
        <p>Projects are shared workspaces: sessions, files, memories and a team chat that every member sees.</p>
        <p class="small">Create one on the left, or accept an invitation.</p>
      </div>
    {/if}
  </div>
</div>

<style>
  .split { flex: 1; min-height: 0; display: flex; }
  .plist { width: 280px; flex: none; border-right: 1px solid var(--border-soft); overflow-y: auto; padding: 8px; display: flex; flex-direction: column; gap: 2px; }
  .pitem { width: 100%; justify-content: flex-start; gap: 9px; background: transparent; border: none; box-shadow: none; padding: 6px 8px; min-height: 40px; }
  .pitem:hover { background: var(--hover); }
  .pitem.sel { background: var(--surface); box-shadow: var(--shadow-1); }
  .invite { display: flex; align-items: center; gap: 8px; padding: 6px 8px; border-radius: 6px; background: var(--accent-soft); margin-bottom: 4px; }
  .newp { display: flex; gap: 6px; padding: 10px 2px 2px; }
  .newp input { padding: 4px 8px; }
  .detail { flex: 1; min-width: 0; overflow-y: auto; }
  .detail .container { gap: 20px; }
  h2 { margin: 0; font-size: 20px; }
  h3 { margin: 0 0 8px; font-size: 14px; }
  .dhead { align-items: flex-end; }
  .members { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 6px; }
  .member { display: flex; align-items: center; gap: 8px; padding: 6px 8px; border: 1px solid var(--border-soft); border-radius: 8px; background: var(--surface); min-width: 0; }
  .member .ellipsize { flex: 1; }
  .avatar { width: 24px; height: 24px; border-radius: 50%; display: grid; place-items: center; flex: none;
    background: color-mix(in srgb, var(--purple) 20%, transparent); color: var(--purple); font-weight: 700; font-size: 12px; }
  .danger-zone { border-top: 1px solid var(--border-soft); padding-top: 14px; }
  .empty { max-width: 440px; margin: 16vh auto; text-align: center; display: flex; flex-direction: column; align-items: center; }
</style>
