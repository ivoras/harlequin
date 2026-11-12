<script lang="ts">
  import { activeProject, view } from "../lib/stores";
  import { api } from "../lib/api";
  import { switchToProject } from "../lib/project";
  import type { ChatUnreadProject } from "../lib/types";
  import Icon from "./Icon.svelte";
  import { ui } from "./state.svelte";
  import { unread } from "./unread.svelte";

  // Top-right "unread messages" indicator: the number of unread team-chat
  // messages across projects, and a drop-down listing them per project.
  let open = $state(false);

  const time = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

  // Go to the project: make it the active one and show its page with the team
  // chat beside it. The chat pane marks the messages read once it shows them.
  async function goTo(u: ChatUnreadProject) {
    open = false;
    let p = ui.projects.find((x) => x.id === u.project_id);
    if (!p) {
      await ui.loadProjects();
      p = ui.projects.find((x) => x.id === u.project_id);
    }
    if (!p) return;
    if ($activeProject?.id !== p.id) await switchToProject(p);
    view.set("projects");
    ui.openInspector("team");
  }
</script>

<span class="menu-wrap unread-wrap">
  <button class="icon-btn unread-btn" class:has={unread.total > 0} onclick={() => { open = !open; if (open) unread.refresh(); }}
    title={unread.total ? `${unread.total} unread team chat message${unread.total === 1 ? "" : "s"}` : "No unread team chat messages"}
    aria-label="Unread messages" aria-haspopup="menu" aria-expanded={open}>
    <Icon name="message" />
    {#if unread.total > 0}<span class="unread-badge">{unread.total > 99 ? "99+" : unread.total}</span>{/if}
  </button>
  {#if open}
    <div class="menu-click-away" role="presentation" onclick={() => (open = false)}></div>
    <div class="popover unread-pop" role="menu">
      {#if unread.projects.length === 0}
        <div class="faint small" style="padding: 8px;">No unread team chat messages.</div>
      {/if}
      {#each unread.projects as u (u.project_id)}
        <button class="unread-sec" role="menuitem" onclick={() => goTo(u)} title="Open {u.project_name}">
          <span class="unread-head"><b class="grow">{u.project_name}</b><span class="pill">{u.count}</span></span>
          {#each u.messages as m (m.id)}
            <span class="unread-msg"><span class="faint">{time(m.created_at)}</span> <b>{m.email.split("@")[0]}</b> {m.snippet}</span>
          {/each}
          {#if u.count > u.messages.length}<span class="faint small">+ {u.count - u.messages.length} older</span>{/if}
        </button>
      {/each}
    </div>
  {/if}
</span>

<style>
  .unread-wrap { position: relative; }
  .unread-btn { position: relative; }
  .unread-btn.has { color: var(--accent-text); }
  .unread-badge { position: absolute; top: -2px; right: -4px; min-width: 16px; height: 16px; padding: 0 4px; border-radius: 8px;
    background: var(--danger); color: #fff; font-size: 10px; font-weight: 600; line-height: 16px; text-align: center; }
  .unread-pop { right: 0; top: calc(100% + 4px); width: 360px; max-width: 90vw; }
  .unread-sec { display: flex; flex-direction: column; align-items: stretch; gap: 3px; width: 100%; text-align: left; padding: 7px 8px;
    background: transparent; border: none; box-shadow: none; border-radius: 6px; height: auto; }
  .unread-sec + .unread-sec { border-top: 1px solid var(--border-soft); border-radius: 0 0 6px 6px; }
  .unread-sec:hover { background: var(--hover); }
  .unread-head { display: flex; align-items: center; gap: 8px; }
  .grow { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .unread-msg { font-size: 12.5px; color: var(--muted); word-break: break-word; }
</style>
