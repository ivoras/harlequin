<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { activeProject, projectSheet, toast } from "../lib/stores";
  import { switchToProject, leaveActiveProject, projectBylines } from "../lib/project";
  import { uploadWithProgress, ingestLabel } from "../lib/ingest";
  import { viewDocument } from "../lib/docview.svelte";
  import type { Project, ProjectMember, Document } from "../lib/types";

  // Project picker + a document file manager for the picked project. The pick
  // is local to this tab (browsing a project's documents does not switch the
  // chat workspace); it starts on the globally active project when set.
  let projects = $state<Project[]>([]);
  // "by <email>" suffixes for projects with duplicate names.
  let bylines = $derived(projectBylines(projects));
  let selected = $state<number>(0);
  let docs = $state<Document[]>([]);
  let loadingDocs = $state(false);
  let uploading = $state(false);
  let uploadLabel = $state("");
  let title = $state("");
  let content = $state("");
  let fileEl = $state<HTMLInputElement>();
  let members = $state<ProjectMember[]>([]);
  let inviteEmail = $state("");
  let directory = $state<{ id: number; email: string }[]>([]);
  // datalist suggestions: everyone except current members of the picked project
  let suggestions = $derived(directory.filter((u) => !members.some((m) => m.user_id === u.id)));

  onMount(async () => {
    try {
      projects = await api.listProjects();
      const active = $activeProject?.id;
      if (active && projects.some((p) => p.id === active)) selected = active;
      else if (projects.length) selected = projects[0].id;
    } catch (e) {
      toast((e as Error).message, "error");
    }
    try {
      directory = await api.userDirectory();
    } catch {
      directory = []; // autocomplete is best-effort; inviting by typed email still works
    }
  });

  // (Re)load the picked project's documents. listDocuments fuses scopes, so
  // keep only the project corpus here — this manager is about project files.
  $effect(() => {
    const id = selected;
    if (!id) {
      docs = [];
      return;
    }
    loadingDocs = true;
    api
      .listDocuments(id)
      .then((all) => {
        docs = all.filter((d) => d.scope === "project");
      })
      .catch((e) => toast((e as Error).message, "error"))
      .finally(() => (loadingDocs = false));
    members = [];
    api.getProject(id).then((p) => (members = p.members ?? [])).catch(() => {});
  });

  async function reload() {
    if (!selected) return;
    docs = (await api.listDocuments(selected)).filter((d) => d.scope === "project");
  }

  async function invite() {
    const email = inviteEmail.trim();
    if (!email || !selected) return;
    try {
      await api.inviteToProject(selected, email);
      inviteEmail = "";
      toast("invited " + email + " — they'll get a notification to accept");
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  async function upload(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    if (!file || !selected) return;
    uploading = true;
    try {
      const d = await uploadWithProgress(file, { scope: "project", projectID: selected }, (p) => (uploadLabel = ingestLabel(p)));
      toast(`ingested "${d.title}"`);
      await reload();
    } catch (err) {
      toast((err as Error).message, "error");
    } finally {
      uploading = false;
      uploadLabel = "";
      if (fileEl) fileEl.value = ""; // allow re-selecting the same file
    }
  }

  async function addText() {
    if (!content.trim() || !selected) return;
    try {
      await api.createDocument({
        title: title.trim() || "Untitled",
        uri: "",
        mime: "text/plain",
        content,
        scope: "project",
        project_id: selected,
      });
      title = "";
      content = "";
      toast("ingested");
      await reload();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  async function del(id: number) {
    try {
      await api.deleteDocument(id, "project", selected);
      docs = docs.filter((d) => d.id !== id);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
</script>

<section class="panel">
  <div class="container col">
    <div class="row">
      <h3>Projects</h3>
      <span class="spacer"></span>
      {#if selected && selected !== $activeProject?.id}
        <button class="primary small" onclick={() => {
          const p = projects.find((x) => x.id === selected);
          if (p) switchToProject(p);
        }}>Switch to {projects.find((x) => x.id === selected)?.name}</button>
      {:else if selected && selected === $activeProject?.id}
        <button class="small" onclick={leaveActiveProject}>Leave project</button>
      {/if}
      <button class="small" onclick={() => projectSheet.set(true)}>Manage…</button>
    </div>

    {#if projects.length === 0}
      <div class="card muted small">
        You are not a member of any project yet. Use <b>Manage…</b> to create one
        or accept an invite.
      </div>
    {:else}
      <div class="picker row" style="flex-wrap:wrap; gap:6px;">
        {#each projects as p}
          <button
            class="small proj"
            class:active={selected === p.id}
            onclick={() => (selected = p.id)}
          >
            <span class="ellipsize">{p.name}</span>
            {#if bylines.get(p.id)}<span class="muted byline ellipsize">{bylines.get(p.id)}</span>{/if}
            {#if $activeProject?.id === p.id}<span class="dot" title="active in chat"></span>{/if}
          </button>
        {/each}
      </div>

      <h3 class="muted small">Members</h3>
      <div class="row" style="flex-wrap:wrap; gap:6px; align-items:center;">
        {#each members as m (m.user_id)}
          <span class="pill ellipsize" style="max-width:100%;">{m.email}</span>
        {/each}
        {#if members.length === 0}<span class="muted small">Loading…</span>{/if}
      </div>
      <div class="row" style="gap:6px;">
        <input placeholder="Invite by email" list="invite-directory" bind:value={inviteEmail}
          onkeydown={(e) => e.key === "Enter" && invite()} style="flex:1; min-width:0;" />
        <datalist id="invite-directory">
          {#each suggestions as u (u.id)}<option value={u.email}></option>{/each}
        </datalist>
        <button class="small" onclick={invite} disabled={!inviteEmail.trim()}>Invite</button>
      </div>

      <h3 class="muted small">Documents</h3>
      {#if loadingDocs}
        <div class="muted small">Loading…</div>
      {:else}
        <div class="list">
          {#each docs as d}
            <div class="card row">
              <div class="col" style="flex:1; min-width:0; gap:1px;">
                <strong>{d.title}</strong>
                <span class="muted small">
                  {d.mime} · {new Date(d.created_at).toLocaleDateString()}
                  {#if d.chunks} · {d.chunks} chunks{/if}
                </span>
              </div>
              <button class="ghost small" onclick={() => viewDocument(d, selected)}>View</button>
              <button class="ghost danger small" onclick={() => del(d.id)} aria-label="Delete">✕</button>
            </div>
          {/each}
          {#if docs.length === 0}<div class="muted small">No documents in this project.</div>{/if}
        </div>
      {/if}

      <div class="card col">
        <div class="row" style="align-items:center; gap:8px;">
          <input type="file" accept=".pdf,.docx,.txt,.md,application/pdf,application/vnd.openxmlformats-officedocument.wordprocessingml.document,text/plain" bind:this={fileEl} onchange={upload} disabled={uploading} />
          {#if uploading}<span class="muted small"><span class="spin">⟳</span> {uploadLabel}</span>{/if}
        </div>
        <div class="muted small">Upload a PDF or text file into this project's corpus.</div>
        <hr />
        <input placeholder="title" bind:value={title} />
        <textarea rows="3" placeholder="…or paste text to ingest" bind:value={content}></textarea>
        <button class="primary" onclick={addText} disabled={!content.trim()}>Ingest text</button>
      </div>
    {/if}
  </div>
</section>

<style>
  .picker .proj { display: inline-flex; align-items: center; gap: 6px; max-width: 100%; }
  /* the name yields last, the byline shrinks first; on wide screens neither
     truncates unless genuinely long */
  .picker .proj .ellipsize { flex: 0 1 auto; }
  .picker .proj .byline { flex-shrink: 10; font-size: 0.85em; }
  .picker .proj.active {
    color: var(--accent);
    border-color: var(--accent-dim);
    background: var(--surface-3);
  }
  .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--accent);
  }
</style>
