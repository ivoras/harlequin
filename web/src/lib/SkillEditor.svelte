<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "./api";
  import { toast, user, activeProject } from "./stores";
  import { isElevated } from "./types";

  // A small text editor: line-number gutter, active-line highlight, and the
  // usual textarea editing operations. Loads one skill file and saves it back
  // into the chosen scope.
  let {
    name,
    path,
    hat = false,
    onClose,
  }: { name: string; path: string; hat?: boolean; onClose: () => void } = $props();

  let content = $state("");
  let scope = $state("user"); // save target; defaults to the resolved scope once loaded
  let fromScope = $state("");
  // Scopes this user may save into: user always, shared when elevated,
  // project when one is active. The select asks explicitly — a save into a
  // scope shadowed by a deeper one would otherwise be silently invisible.
  let writable = $derived([
    "user",
    ...(isElevated($user?.role) ? ["shared"] : []),
    ...($activeProject ? ["project"] : []),
  ]);
  let activeLine = $state(0);
  let scrollTop = $state(0);
  let loading = $state(true);
  let saving = $state(false);
  let ta = $state<HTMLTextAreaElement>();
  let gutter = $state<HTMLDivElement>();

  const lineHeight = 20;

  let lineCount = $derived(content.split("\n").length);

  onMount(async () => {
    try {
      if (hat) {
        content = (await api.getHatFile(name, path)).content;
        fromScope = "shared"; // hats are shared-only
      } else {
        const f = await api.getSkillFile(name, path);
        content = f.content;
        fromScope = f.scope;
        if (writable.includes(f.scope)) scope = f.scope;
      }
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      loading = false;
    }
  });

  function caretLine() {
    const pos = ta?.selectionStart ?? 0;
    activeLine = content.slice(0, pos).split("\n").length - 1;
  }

  function onScroll() {
    scrollTop = ta.scrollTop;
    if (gutter) gutter.scrollTop = scrollTop;
  }

  async function save() {
    saving = true;
    try {
      if (hat) await api.putHatFile(name, path, content);
      else await api.putSkillFile(name, path, scope, content);
      toast(`saved ${name}/${path}`);
      onClose();
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      saving = false;
    }
  }

  function onKeydown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key === "s") {
      e.preventDefault();
      save();
    } else if (e.key === "Escape") {
      onClose();
    }
  }
</script>

<div class="scrim" role="presentation" onclick={onClose}></div>
<aside class="sheet right skill-editor">
  <header>
    <strong class="mono">{hat ? "hat" : "skill"}://{name}/{path}</strong>
    <span class="spacer"></span>
    {#if !hat}
      <label class="small muted">save to
        <select bind:value={scope}>
          {#each writable as w}
            <option value={w}>{w}{w === fromScope ? " (resolved)" : ""}</option>
          {/each}
        </select>
      </label>
    {/if}
    <button class="small" onclick={save} disabled={saving || loading}>Save</button>
    <button class="ghost" onclick={onClose}>Close</button>
  </header>
  <div class="meta muted small">resolved from: {fromScope || "-"} · Ctrl-S save · Esc close</div>
  {#if loading}
    <div class="muted small" style="padding:12px;">Loading…</div>
  {:else}
    <div class="editor" style="--lh:{lineHeight}px">
      <div class="gutter mono" bind:this={gutter}>
        {#each Array(lineCount) as _, i}
          <div class="ln" class:active={i === activeLine}>{i + 1}</div>
        {/each}
      </div>
      <div class="ta-wrap">
        <div class="active-stripe" style="top:{activeLine * lineHeight - scrollTop}px"></div>
        <textarea
          class="mono"
          bind:this={ta}
          bind:value={content}
          onkeyup={caretLine}
          onclick={caretLine}
          oninput={caretLine}
          onscroll={onScroll}
          onkeydown={onKeydown}
          spellcheck="false"
          wrap="off"
        ></textarea>
      </div>
    </div>
  {/if}
</aside>

<style>
  .skill-editor {
    display: flex;
    flex-direction: column;
    width: min(760px, 92vw);
  }
  .meta {
    padding: 2px 12px 8px;
  }
  .editor {
    flex: 1;
    display: flex;
    min-height: 0;
    border-top: 1px solid var(--border, #333);
  }
  .gutter {
    overflow: hidden;
    text-align: right;
    padding: 8px 6px 8px 8px;
    color: var(--muted, #888);
    user-select: none;
    background: rgba(255, 255, 255, 0.02);
  }
  .gutter .ln {
    height: var(--lh);
    line-height: var(--lh);
    font-size: 13px;
  }
  .gutter .ln.active {
    color: var(--accent, #6cf);
    font-weight: 700;
  }
  .ta-wrap {
    position: relative;
    flex: 1;
    overflow: hidden;
  }
  .active-stripe {
    position: absolute;
    left: 0;
    right: 0;
    height: var(--lh);
    background: rgba(255, 255, 255, 0.05);
    pointer-events: none;
  }
  textarea {
    position: relative;
    width: 100%;
    height: 100%;
    border: 0;
    resize: none;
    outline: none;
    padding: 8px;
    margin: 0;
    background: transparent;
    color: var(--text, #ddd);
    line-height: var(--lh);
    font-size: 13px;
    white-space: pre;
    overflow: auto;
  }
</style>
