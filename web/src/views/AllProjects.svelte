<script lang="ts">
  // Owner-only read-only overview of every project (the sidebar shows only
  // those you belong to): join one, add a user by email, remove a member.
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { toast } from "../lib/stores";
  import { user } from "../lib/stores";
  import { ui } from "../desktop/state.svelte";
  import type { Project } from "../lib/types";

  let projects = $state<Project[]>([]);
  let loading = $state(true);
  let emails = $state<Record<number, string>>({});

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

  const isMember = (p: Project) => !!p.members?.some((m) => m.user_id === $user?.id);

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
  function add(p: Project) {
    const email = (emails[p.id] ?? "").trim();
    if (!email) return;
    act(() => api.adminAddProjectMember(p.id, email), "Member added").then(() => (emails[p.id] = ""));
  }
</script>

<section class="panel">
  <div class="container col">
    <div class="muted small">All {projects.length} projects. You only see the contents of those you join.</div>
    {#if loading}<div class="muted">Loading…</div>{/if}
    {#each projects as p (p.id)}
      <div class="card col">
        <div class="row">
          <b>{p.name}</b>
          <span class="muted small">#{p.id} · created {p.created_at.slice(0, 10)}</span>
          <span class="spacer"></span>
          {#if isMember(p)}
            <span class="muted small">member</span>
          {:else}
            <button class="btn" onclick={() => act(() => api.adminJoinProject(p.id), `Joined ${p.name}`)}>Join</button>
          {/if}
        </div>
        {#each p.members ?? [] as m (m.user_id)}
          <div class="row small">
            <span>{m.email}</span>
            {#if m.user_id === p.created_by}<span class="muted">creator</span>{/if}
            <span class="spacer"></span>
            {#if m.user_id !== p.created_by && m.user_id !== $user?.id}
              <button class="btn" onclick={() => remove(p, m.user_id, m.email)}>Remove</button>
            {/if}
          </div>
        {/each}
        <form class="row" onsubmit={(e) => { e.preventDefault(); add(p); }}>
          <input type="email" placeholder="Add user by email" bind:value={emails[p.id]} />
          <button class="btn" type="submit">Add</button>
        </form>
      </div>
    {/each}
  </div>
</section>
