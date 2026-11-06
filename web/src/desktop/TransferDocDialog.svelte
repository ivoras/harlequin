<script lang="ts">
  // Modal: pick the project to move or copy a project document into.
  import { api } from "../lib/api";
  import { toast } from "../lib/stores";
  import type { Document, Project } from "../lib/types";
  import Icon from "./Icon.svelte";

  let { doc, from, projects, mode, onclose }: {
    doc: Document; from: Project; projects: Project[]; mode: "move" | "copy"; onclose: () => void;
  } = $props();

  const targets = $derived(projects.filter((p) => p.id !== from.id));
  let target = $state(0);
  let busy = $state(false);
  const verb = $derived(mode === "move" ? "Move" : "Copy");

  async function run() {
    const to = targets.find((p) => p.id === target);
    if (!to) return;
    busy = true;
    try {
      await api.transferProjectDoc(from.id, doc.id, to.id, mode);
      toast(`${mode === "move" ? "moved" : "copied"} "${doc.title}" to ${to.name}`);
      onclose();
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      busy = false;
    }
  }
</script>

<svelte:window onkeydown={(e) => e.key === "Escape" && !busy && onclose()} />

<div class="modal-scrim" role="presentation" onclick={() => !busy && onclose()}></div>
<div class="modal" role="dialog" aria-modal="true" aria-label="{verb} file to another project">
  <header>
    <Icon name="folder" />
    <strong class="ellipsize">{verb} “{doc.title}” to another project</strong>
    <span class="spacer"></span>
    <button class="icon-btn" onclick={onclose} aria-label="Close" disabled={busy}><Icon name="x" /></button>
  </header>
  <div class="tbody">
    {#if targets.length === 0}
      <div class="muted small">You aren't a member of any other project. Create or join one first.</div>
    {:else}
      <div class="muted small">From <b>{from.name}</b> to:</div>
      <div class="tlist" role="radiogroup" aria-label="Target project">
        {#each targets as p (p.id)}
          <label class="topt" class:sel={target === p.id}>
            <input type="radio" name="transfer-target" value={p.id} bind:group={target} />
            <span class="ellipsize">{p.name}</span>
          </label>
        {/each}
      </div>
      <div class="muted small">
        {#if mode === "move"}
          The file leaves {from.name} for every member there; its text, search index and original file go with it.
          Citations and <code>p{from.id}.{doc.id}</code> references to it stop working.
        {:else}
          The original stays in {from.name}; the copy is independent (editing one doesn't change the other).
        {/if}
      </div>
    {/if}
  </div>
  <footer>
    <button onclick={onclose} disabled={busy}>Cancel</button>
    <button class="primary" onclick={run} disabled={!target || busy}>{busy ? `${verb === "Move" ? "Moving" : "Copying"}…` : verb}</button>
  </footer>
</div>

<style>
  .tbody { padding: 14px 16px; display: flex; flex-direction: column; gap: 10px; overflow-y: auto; }
  .tlist { display: flex; flex-direction: column; gap: 4px; max-height: 40vh; overflow-y: auto; }
  .topt { display: flex; align-items: center; gap: 8px; padding: 7px 10px; border: 1px solid var(--border-soft);
    border-radius: 8px; cursor: pointer; min-width: 0; }
  .topt:hover { background: var(--hover); }
  .topt.sel { border-color: var(--accent-dim); background: var(--accent-soft); }
  footer { display: flex; justify-content: flex-end; gap: 8px; padding: 10px 16px; border-top: 1px solid var(--border-soft); }
</style>
