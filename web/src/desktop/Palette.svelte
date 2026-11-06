<script lang="ts">
  import { tick } from "svelte";
  import { view, activeProject, user, type View } from "../lib/stores";
  import { isOwner } from "../lib/types";
  import { sc } from "../lib/session.svelte";
  import { switchToProject, leaveActiveProject, cleanTitle } from "../lib/project";
  import { ensureSession } from "../lib/workspace";
  import { viewDocument } from "../lib/docview.svelte";
  import { switchUI } from "../lib/uipref";
  import Icon from "./Icon.svelte";
  import { ui, openSession, MOD } from "./state.svelte";
  import { setTheme } from "./theme.svelte";

  // Command palette: fuzzy jump to actions, pages, projects, sessions and files.
  interface Item {
    group: string;
    label: string;
    hint?: string;
    icon: string;
    keys?: string;
    run: () => void | Promise<void>;
  }

  let q = $state("");
  let sel = $state(0);
  let inputEl: HTMLInputElement | undefined = $state();
  let listEl: HTMLDivElement | undefined = $state();

  $effect(() => {
    if (ui.paletteOpen) {
      q = "";
      sel = 0;
      ui.loadProjects();
      tick().then(() => inputEl?.focus());
    }
  });

  const go = (v: View) => () => view.set(v);

  const items = $derived.by<Item[]>(() => {
    const out: Item[] = [
      { group: "Actions", label: "New session", icon: "plus", keys: `${MOD} ⇧ O`, run: async () => { await ensureSession(); view.set("chat"); } },
      { group: "Actions", label: "Upload files…", icon: "upload", hint: `to ${ui.defaultTarget().label}`, run: pickFiles },
      { group: "Actions", label: "Export transcript", icon: "download", run: () => sc.exportTranscript() },
      { group: "Actions", label: "Clear session context", icon: "eraser", run: () => sc.clear() },
      { group: "Actions", label: "Toggle sidebar", icon: "panelLeft", keys: `${MOD} B`, run: () => { ui.sidebarOpen = !ui.sidebarOpen; } },
      { group: "Actions", label: "Toggle side panel", icon: "panelRight", keys: `${MOD} .`, run: () => { ui.inspectorOpen = !ui.inspectorOpen; } },
      { group: "Actions", label: "Theme: light", icon: "sun", run: () => setTheme("light") },
      { group: "Actions", label: "Theme: dark", icon: "moon", run: () => setTheme("dark") },
      { group: "Actions", label: "Theme: follow system", icon: "monitor", run: () => setTheme("system") },
      { group: "Actions", label: "Switch to the mobile version", icon: "phone", run: () => switchUI("mobile") },
      { group: "Go to", label: "Chat", icon: "message", run: go("chat") },
      { group: "Go to", label: "Files", icon: "file", run: go("files") },
      { group: "Go to", label: "Memory", icon: "brain", run: go("memory") },
      { group: "Go to", label: "Skills", icon: "sparkles", run: go("skills") },
      { group: "Go to", label: "Scheduled jobs", icon: "clock", hint: "cron", run: go("cron") },
      { group: "Go to", label: "Projects", icon: "users", run: go("projects") },
      { group: "Go to", label: "Settings", icon: "settings", keys: `${MOD} ,`, run: () => ui.openSettings("general") },
      { group: "Go to", label: "Hats", icon: "hat", hint: "settings", run: () => ui.openSettings("hats") },
      { group: "Go to", label: "MCP servers", icon: "plug", hint: "settings", run: () => ui.openSettings("mcp") },
      { group: "Go to", label: "Config", icon: "key", hint: "settings", run: () => ui.openSettings("config") },
      { group: "Go to", label: "Usage", icon: "chart", hint: "settings", run: () => ui.openSettings("usage") },
      { group: "Go to", label: "Context usage", icon: "gauge", hint: "side panel", run: () => ui.openInspector("session") },
    ];
    if (isOwner($user?.role)) {
      out.push({ group: "Go to", label: "Users", icon: "users", hint: "settings", run: () => ui.openSettings("users") });
    }
    if ($activeProject) {
      out.push({ group: "Go to", label: "Team chat", icon: "users", hint: $activeProject.name, run: () => ui.openInspector("team") });
      out.push({ group: "Projects", label: "Personal workspace", icon: "user", hint: "leave project", run: leaveActiveProject });
    }
    for (const p of ui.projects) {
      if (p.id === $activeProject?.id) continue;
      out.push({ group: "Projects", label: p.name, icon: "folder", hint: "open workspace", run: () => switchToProject(p) });
    }
    for (const c of ui.sessions) {
      out.push({ group: "Sessions", label: cleanTitle(c.title) || "New session", icon: "message", hint: `#${c.id}`, run: () => openSession(c) });
    }
    for (const d of ui.docs) {
      out.push({ group: "Files", label: d.title, icon: "file", hint: d.scope, run: () => viewDocument(d, d.scope === "project" ? ($activeProject?.id ?? 0) : 0) });
    }
    return out;
  });

  // Subsequence match, scored: contiguous runs and word-start hits rank higher.
  function score(text: string, query: string): number {
    const t = text.toLowerCase();
    const s = query.toLowerCase();
    const at = t.indexOf(s);
    if (at >= 0) return 1000 - at + (at === 0 || t[at - 1] === " " ? 200 : 0);
    let ti = 0;
    let sc = 0;
    let run = 0;
    for (const ch of s) {
      if (ch === " ") continue;
      const i = t.indexOf(ch, ti);
      if (i < 0) return -1;
      run = i === ti ? run + 1 : 0;
      sc += 1 + run * 2 + (i === 0 || t[i - 1] === " " ? 3 : 0);
      ti = i + 1;
    }
    return sc;
  }

  const results = $derived.by(() => {
    const s = q.trim();
    if (!s) {
      // Empty query: actions, pages and the most recent sessions.
      const recent = items.filter((i) => i.group === "Sessions").slice(0, 6);
      return [...items.filter((i) => i.group === "Actions" || i.group === "Go to").slice(0, 12), ...recent];
    }
    return items
      .map((i) => ({ i, s: score(i.label + (i.hint ? " " + i.hint : ""), s) }))
      .filter((x) => x.s >= 0)
      .sort((a, b) => b.s - a.s)
      .slice(0, 40)
      .map((x) => x.i);
  });
  $effect(() => {
    void results;
    sel = 0;
  });

  function close() {
    ui.paletteOpen = false;
  }
  async function run(it: Item) {
    close();
    await it.run();
  }
  function pickFiles() {
    const inp = document.createElement("input");
    inp.type = "file";
    inp.multiple = true;
    inp.onchange = () => inp.files && ui.upload([...inp.files], ui.defaultTarget());
    inp.click();
  }
  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      close();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      sel = Math.min(sel + 1, results.length - 1);
      scrollSel();
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      sel = Math.max(sel - 1, 0);
      scrollSel();
    } else if (e.key === "Enter") {
      e.preventDefault();
      const it = results[sel];
      if (it) run(it);
    }
  }
  function scrollSel() {
    tick().then(() => listEl?.querySelector(".sel")?.scrollIntoView({ block: "nearest" }));
  }
</script>

{#if ui.paletteOpen}
  <div class="modal-scrim" role="presentation" onclick={close}></div>
  <div class="palette" role="dialog" aria-label="Command palette">
    <div class="pinput">
      <Icon name="search" />
      <input bind:this={inputEl} bind:value={q} onkeydown={onKey} placeholder="Search sessions, files, projects and commands…" />
      <kbd>Esc</kbd>
    </div>
    <div class="plist" bind:this={listEl} role="listbox">
      {#each results as it, i}
        {#if i === 0 || results[i - 1].group !== it.group}<div class="menu-label">{it.group}</div>{/if}
        <button class="menu-item" class:sel={i === sel} onmouseenter={() => (sel = i)} onclick={() => run(it)} role="option" aria-selected={i === sel}>
          <Icon name={it.icon} /><span class="grow">{it.label}</span>
          {#if it.hint}<span class="faint small">{it.hint}</span>{/if}
          {#if it.keys}<kbd>{it.keys}</kbd>{/if}
        </button>
      {/each}
      {#if results.length === 0}<div class="faint small" style="padding: 14px;">Nothing matches “{q}”.</div>{/if}
    </div>
  </div>
{/if}

<style>
  .palette { position: fixed; z-index: 36; left: 50%; top: 12vh; transform: translateX(-50%); width: min(640px, 92vw);
    background: var(--surface); border: 1px solid var(--border); border-radius: 14px; box-shadow: var(--shadow-3);
    display: flex; flex-direction: column; max-height: 70vh; overflow: hidden; }
  .pinput { display: flex; align-items: center; gap: 10px; padding: 12px 14px; border-bottom: 1px solid var(--border-soft); color: var(--faint); }
  .pinput input { border: none; box-shadow: none; padding: 2px 0; font-size: 15px; background: transparent; }
  .plist { overflow-y: auto; padding: 4px 6px 8px; }
  .menu-item { min-height: 34px; }
</style>
