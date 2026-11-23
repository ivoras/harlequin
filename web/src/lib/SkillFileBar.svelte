<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "./api";
  import { toast } from "./stores";

  // A minimal file manager for one skill, laid out as a horizontal strip: the
  // skill's files as tabs (SKILL.md first), plus new / rename / delete. Changes
  // are written into `scope` (the editor's "save to" target) the same way a
  // single-file save is: the effective files, changed, become that scope's copy.
  let {
    name,
    current,
    scope,
    readonly = false,
    onOpen,
  }: { name: string; current: string; scope: string; readonly?: boolean; onOpen: (path: string) => void } = $props();

  let files = $state<string[]>([]);
  let mode = $state<"" | "new" | "rename">("");
  let input = $state("");
  let busy = $state(false);
  let field = $state<HTMLInputElement>();

  export async function refresh(): Promise<Record<string, string> | null> {
    try {
      const sk = await api.getSkill(name);
      files = Object.keys(sk.files).sort((a, b) => (a === "SKILL.md" ? -1 : b === "SKILL.md" ? 1 : a.localeCompare(b)));
      return sk.files;
    } catch (e) {
      toast((e as Error).message, "error");
      return null;
    }
  }
  onMount(refresh);

  // The same rules the server applies to skill file paths, checked here so the
  // user sees the problem before anything is sent.
  function pathError(p: string, exclude = ""): string {
    if (!p) return "enter a file name, e.g. reference/notes.md";
    if (p.startsWith("/") || p.includes("\\")) return "use a relative path with / between folders";
    if (p.split("/").some((s) => s === "" || s === "." || s === "..")) return "the path has an empty, . or .. part";
    if (p !== exclude && files.includes(p)) return `${p} already exists`;
    return "";
  }

  function start(m: "new" | "rename") {
    mode = m;
    input = m === "rename" ? current : "";
    queueMicrotask(() => field?.focus());
  }
  function cancel() {
    mode = "";
    input = "";
  }

  async function submit() {
    const p = input.trim();
    const err = pathError(p, mode === "rename" ? current : "");
    if (err) return toast(err, "error");
    if (mode === "rename" && p === current) return cancel();
    busy = true;
    try {
      if (mode === "new") {
        await api.putSkillFile(name, p, scope, "");
      } else {
        const all = await refresh();
        if (!all) return;
        all[p] = all[current] ?? "";
        delete all[current];
        await api.putSkill(name, scope, all);
      }
      toast(mode === "new" ? `created ${name}/${p}` : `renamed to ${name}/${p}`);
      cancel();
      await refresh();
      onOpen(p);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      busy = false;
    }
  }

  async function remove() {
    if (current === "SKILL.md" || !confirm(`Delete ${name}/${current}?`)) return;
    busy = true;
    try {
      const all = await refresh();
      if (!all) return;
      delete all[current];
      await api.putSkill(name, scope, all);
      toast(`deleted ${name}/${current}`);
      await refresh();
      onOpen("SKILL.md");
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      busy = false;
    }
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Enter") {
      e.preventDefault();
      submit();
    } else if (e.key === "Escape") {
      e.stopPropagation();
      cancel();
    }
  }
</script>

<div class="filebar" role="toolbar" aria-label="Skill files">
  <div class="tabs" role="tablist">
    {#each files as f (f)}
      <button
        class="tab mono"
        class:on={f === current}
        role="tab"
        aria-selected={f === current}
        title={f}
        disabled={busy}
        onclick={() => f !== current && onOpen(f)}>{f}</button
      >
    {/each}
  </div>
  {#if !readonly}
    {#if mode}
      <input
        class="mono"
        bind:this={field}
        bind:value={input}
        onkeydown={onKey}
        placeholder={mode === "new" ? "new file, e.g. reference/notes.md" : "new name"}
        aria-label={mode === "new" ? "New file name" : "New name"}
      />
      <button class="small primary" onclick={submit} disabled={busy}>{mode === "new" ? "Create" : "Rename"}</button>
      <button class="small ghost" onclick={cancel} disabled={busy}>Cancel</button>
    {:else}
      <button class="small" onclick={() => start("new")} disabled={busy} title="Add a file to this skill">+ New file</button>
      <button class="small" onclick={() => start("rename")} disabled={busy || current === "SKILL.md"} title={current === "SKILL.md" ? "SKILL.md keeps its name" : `Rename ${current}`}>Rename</button>
      <button class="small danger" onclick={remove} disabled={busy || current === "SKILL.md"} title={current === "SKILL.md" ? "SKILL.md cannot be deleted" : `Delete ${current}`}>Delete</button>
    {/if}
  {/if}
</div>

<style>
  .filebar {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--border);
    background: var(--surface-2);
    border-radius: 14px 0 0 0;
  }
  .tabs {
    display: flex;
    gap: 4px;
    flex: 1;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: thin;
  }
  .tab {
    flex: none;
    min-height: 26px;
    padding: 2px 10px;
    font-size: 12.5px;
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    box-shadow: none;
    background: transparent;
    border-color: transparent;
    color: var(--muted);
  }
  .tab:hover {
    background: var(--hover);
    color: var(--text);
  }
  .tab.on {
    background: var(--surface);
    border-color: var(--border);
    color: var(--accent-text);
    font-weight: 600;
  }
  input {
    flex: 0 1 240px;
    min-width: 120px;
    font-size: 12.5px;
  }
</style>
