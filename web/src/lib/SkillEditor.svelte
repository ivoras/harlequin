<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { api } from "./api";
  import { toast, user, activeProject, wornHat } from "./stores";
  import { isElevated } from "./types";
  import { hatOverlays, hatOverrideNote } from "./hats";
  import { scopeLabel } from "./scope";
  import { currentUI } from "./uipref";
  import SkillFileBar from "./SkillFileBar.svelte";

  // A small text editor: line-number gutter, active-line highlight, and the
  // usual textarea editing operations. Loads one skill file and saves it back
  // into the chosen scope. readonly turns it into a viewer (e.g. a hat file
  // for a user who may wear the hat but not edit it).
  let {
    name,
    path,
    hat = false,
    readonly = false,
    initial,
    onClose,
  }: { name: string; path: string; hat?: boolean; readonly?: boolean; initial?: string; onClose: () => void } = $props();

  // Set when the worn hat overrides this skill, so edits here are invisible.
  let overriddenBy = $state("");

  // The desktop UI shows a file bar for skills: the open file can change.
  // Read once: the editor is mounted per opening and then tracks the file itself.
  const withFiles = untrack(() => !hat) && currentUI() === "desktop";
  let curPath = $state(untrack(() => path));
  let loaded = $state(""); // content as last loaded or saved, for the unsaved-changes check
  let bar = $state<SkillFileBar>();

  let content = $state("");
  let scope = $state("user"); // save target; defaults to the resolved scope once loaded
  let fromScope = $state("");
  // Scopes this user may save into: personal always, shared when elevated,
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
    const worn = $wornHat;
    if (!hat && worn) hatOverlays(worn).then((ov) => (overriddenBy = ov.includes(name) ? worn : ""));
    if (initial !== undefined) {
      // Caller-provided starting content (e.g. a hat prompt seeded from the
      // default system prompt template); saves still go to the real file.
      content = loaded = initial;
      fromScope = hat ? "shared" : fromScope;
      loading = false;
      return;
    }
    await loadFile(path);
  });

  async function loadFile(p: string) {
    loading = true;
    try {
      if (hat) {
        content = (await api.getHatFile(name, p)).content;
        fromScope = "shared"; // hats are shared-only
      } else {
        const f = await api.getSkillFile(name, p);
        content = f.content;
        fromScope = f.scope;
        if (writable.includes(f.scope)) scope = f.scope;
      }
      loaded = content;
      curPath = p;
      activeLine = 0;
      scrollTop = 0;
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      loading = false;
    }
  }

  // From the file bar: switch files, asking before unsaved edits are lost.
  async function openFile(p: string) {
    if (p !== curPath && content !== loaded && !confirm(`Discard unsaved changes to ${curPath}?`)) return;
    await loadFile(p);
  }

  function caretLine() {
    const pos = ta?.selectionStart ?? 0;
    activeLine = content.slice(0, pos).split("\n").length - 1;
  }

  function onScroll() {
    scrollTop = ta?.scrollTop ?? 0;
    if (gutter) gutter.scrollTop = scrollTop;
  }

  async function save() {
    if (readonly) return;
    saving = true;
    try {
      if (hat) await api.putHatFile(name, curPath, content);
      else await api.putSkillFile(name, curPath, scope, content);
      toast(overriddenBy ? `saved ${name}/${curPath} — not used while you wear the “${overriddenBy}” hat` : `saved ${name}/${curPath}`, overriddenBy ? "error" : "info");
      loaded = content;
      // With the file bar the editor stays open to edit the skill's other files.
      if (withFiles) {
        fromScope = scope;
        await bar?.refresh();
      } else onClose();
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
  {#if withFiles}
    <SkillFileBar bind:this={bar} {name} current={curPath} {scope} {readonly} onOpen={openFile} />
  {/if}
  <header>
    <strong class="mono">{hat ? "hat" : "skill"}://{name}/{curPath}{content !== loaded ? " •" : ""}</strong>
    <span class="spacer"></span>
    {#if readonly}
      <span class="pill">read-only</span>
    {:else if !hat}
      <label class="small muted">save to
        <select bind:value={scope}>
          {#each writable as w}
            <option value={w}>{scopeLabel(w)}{w === fromScope ? " (resolved)" : ""}</option>
          {/each}
        </select>
      </label>
    {/if}
    {#if !readonly}<button class="small" onclick={save} disabled={saving || loading}>Save</button>{/if}
    <button class="ghost" onclick={onClose}>Close</button>
  </header>
  {#if overriddenBy}<div class="hat-warn small">{hatOverrideNote(overriddenBy)}</div>{/if}
  <div class="meta muted small">{readonly ? "read-only · Esc close" : `resolved from: ${fromScope || "-"} · Ctrl-S save · Esc close`}</div>
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
          {readonly}
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
  .hat-warn {
    margin: 4px 12px 6px;
    padding: 6px 10px;
    border-radius: 6px;
    border: 1px solid var(--warn, #c90);
    color: var(--warn, #c90);
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
