<script lang="ts">
  // Owner-only overview of every project (the sidebar shows only those you
  // belong to): members, files with downloads, join, add/remove users.
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast, user } from "../lib/stores";
  import { ui } from "../desktop/state.svelte";
  import type { Document, Project } from "../lib/types";

  let projects = $state<Project[]>([]);
  let loading = $state(true);
  let emails = $state<Record<number, string>>({});
  let files = $state<Record<number, Document[]>>({});

  async function load() {
    try {
      projects = await api.adminProjects();
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      loading = false;
    }
  }
  onMount(load);

  async function loadFiles(p: Project) {
    if (files[p.id]) return;
    try {
      files[p.id] = await api.adminProjectDocs(p.id);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }

  const isMember = (p: Project) => !!p.members?.some((m) => m.user_id === $user?.id);
  const size = (n: number) => `${n} chunk${n === 1 ? "" : "s"}`;

  async function act(fn: () => Promise<unknown>, ok: string) {
    try {
      await fn();
      toast(ok);
      await load();
      ui.loadProjects();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  function remove(p: Project, uid: number, email: string) {
    if (!confirm(`Remove ${email} from "${p.name}"?`)) return;
    act(() => api.removeProjectMember(p.id, uid), "Member removed");
  }
  async function add(p: Project) {
    const email = (emails[p.id] ?? "").trim();
    if (!email) return;
    await act(() => api.adminAddProjectMember(p.id, email), "Member added");
    emails[p.id] = "";
  }

  function save(blob: Blob, name: string) {
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
  }
  // Stored original when there is one, else the extracted text as Markdown.
  async function download(p: Project, d: Document) {
    try {
      const f = await api.fetchDocumentFile(d.id, "project", p.id);
      save(f.blob, d.original_name || f.filename || d.title);
    } catch {
      try {
        const c = await api.getDocumentContent(d.id, "project", p.id);
        save(new Blob([c.content], { type: "text/markdown;charset=utf-8" }), (d.title || "document") + ".md");
      } catch (e) {
        toast((e as Error).message, "error");
      }
    }
  }
</script>

<section class="panel">
  <div class="container ap">
    <p class="muted small">
      All {projects.length} projects. Joining one adds it to your sidebar; files can be downloaded without joining.
    </p>
    {#if loading}<div class="muted">Loading…</div>{/if}
    {#each projects as p (p.id)}
      <div class="card ap-card">
        <header>
          <div class="ap-title">
            <b>{p.name}</b>
            <span class="muted small">#{p.id} · {p.created_by_email || `user ${p.created_by}`} · {p.created_at.slice(0, 10)}</span>
          </div>
          {#if isMember(p)}
            <span class="muted small">You're a member</span>
          {:else}
            <button class="primary" onclick={() => act(() => api.adminJoinProject(p.id), `Joined ${p.name}`)}>Join</button>
          {/if}
        </header>

        <h4>Members ({p.members?.length ?? 0})</h4>
        <ul>
          {#each p.members ?? [] as m (m.user_id)}
            <li>
              <span class="grow">{m.email}{#if m.user_id === p.created_by} <span class="muted small">creator</span>{/if}</span>
              {#if m.user_id !== p.created_by && m.user_id !== $user?.id}
                <button class="ghost danger" onclick={() => remove(p, m.user_id, m.email)}>Remove</button>
              {/if}
            </li>
          {/each}
        </ul>
        <form onsubmit={(e) => { e.preventDefault(); add(p); }}>
          <input type="email" placeholder="Add user by email" bind:value={emails[p.id]} />
          <button type="submit">Add</button>
        </form>

        <details ontoggle={(e) => (e.currentTarget as HTMLDetailsElement).open && loadFiles(p)}>
          <summary>Files{files[p.id] ? ` (${files[p.id].length})` : ""}</summary>
          {#if !files[p.id]}
            <div class="muted small">Loading…</div>
          {:else if files[p.id].length === 0}
            <div class="muted small">No files.</div>
          {:else}
            <ul>
              {#each files[p.id] as d (d.id)}
                <li>
                  <span class="grow">{d.title} <span class="muted small">{size(d.chunks ?? 0)}</span></span>
                  <button onclick={() => download(p, d)}>Download</button>
                </li>
              {/each}
            </ul>
          {/if}
        </details>
      </div>
    {/each}
  </div>
</section>

<style>
  .ap { display: flex; flex-direction: column; gap: 12px; }
  .ap-card { display: flex; flex-direction: column; gap: 8px; padding: 12px 14px; }
  header { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .ap-title { display: flex; flex-direction: column; min-width: 0; }
  h4 { margin: 4px 0 0; font-size: 12px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--muted, inherit); }
  ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; }
  li { display: flex; align-items: center; gap: 8px; }
  .grow { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  form { display: flex; gap: 8px; align-items: center; }
  form input { flex: 1; min-width: 0; }
  button { flex: none; white-space: nowrap; }
  summary { cursor: pointer; font-weight: 600; margin-bottom: 6px; }
</style>
