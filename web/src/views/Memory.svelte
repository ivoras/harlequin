<script lang="ts">
  import { api } from "../lib/api";
  import { user, toast } from "../lib/stores";
  import { isElevated } from "../lib/types";
  import type { Memory, MemoryConflict } from "../lib/types";

  let scope = $state<"user" | "shared">("user");
  let mems = $state<Memory[]>([]);
  let q = $state("");
  let addText = $state("");
  let conflicts = $state<MemoryConflict[]>([]);
  let showConflicts = $state(false);

  // Reload when the scope tab changes.
  $effect(() => {
    void scope;
    loadList();
  });
  async function loadList() {
    try {
      mems = await api.listMemory(scope);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function find() {
    if (!q.trim()) return loadList();
    try {
      mems = await api.findMemory(q.trim());
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function add() {
    if (!addText.trim()) return;
    try {
      await api.createMemory({ scope, content: addText.trim() });
      addText = "";
      toast("stored");
      await loadList();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function del(id: string) {
    try {
      await api.deleteMemory(id);
      mems = mems.filter((m) => m.id !== id);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function loadConflicts() {
    try {
      conflicts = await api.listMemoryConflicts();
      showConflicts = true;
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function resolve(id: string) {
    try {
      await api.resolveMemoryConflict(id);
      conflicts = conflicts.filter((c) => c.id !== id);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  let canShared = $derived(isElevated($user?.role));
</script>

<section class="panel">
  <div class="container col">
    <div class="row"><h3>Memory</h3><span class="spacer"></span><button class="small" onclick={loadConflicts}>Conflicts</button></div>
    <div class="tabs">
      <button class:active={scope === "user"} onclick={() => (scope = "user")}>Personal</button>
      <button class:active={scope === "shared"} onclick={() => (scope = "shared")}>Shared</button>
    </div>
    <div class="row">
      <input placeholder="search memories…" bind:value={q} onkeydown={(e) => e.key === "Enter" && find()} />
      <button class="small" onclick={find}>Find</button>
    </div>
    <div class="list">
      {#each mems as m}
        <div class="card col" style="gap:4px;">
          <div class="wrap">{m.content}</div>
          <div class="row small muted">
            <span class="pill">{m.id}</span>
            {#each m.slots ?? [] as s}<span class="pill" title={s.value}>{s.key}</span>{/each}
            {#if m.pinned}<span title="pinned">📌</span>{/if}
            <span class="spacer"></span>
            <button class="ghost danger small" onclick={() => del(m.id)}>Delete</button>
          </div>
        </div>
      {/each}
      {#if mems.length === 0}<div class="muted small">Nothing here.</div>{/if}
    </div>
    <div class="card col">
      <textarea rows="2" placeholder={`Add a ${scope} memory…`} bind:value={addText}></textarea>
      <button class="primary" onclick={add} disabled={!addText.trim() || (scope === "shared" && !canShared)}>Store</button>
      {#if scope === "shared" && !canShared}<div class="muted small">Only owner/admin can add shared memories.</div>{/if}
    </div>
  </div>
</section>

{#if showConflicts}
  <div class="scrim" role="presentation" onclick={() => (showConflicts = false)}></div>
  <aside class="sheet bottom">
    <header><strong>Conflicts</strong><span class="spacer"></span><button class="ghost" onclick={() => (showConflicts = false)}>Close</button></header>
    <div class="body list">
      {#each conflicts as c}
        <div class="card col small">
          <div><span class="pill">{c.relationship}</span> confidence {c.confidence}</div>
          <div class="wrap muted">A ({c.memory_a}): {c.content_a}</div>
          <div class="wrap muted">B ({c.memory_b}): {c.content_b}</div>
          <div class="muted">{c.reason}</div>
          <button class="small" onclick={() => resolve(c.id)}>Mark resolved</button>
        </div>
      {/each}
      {#if conflicts.length === 0}<div class="muted small">No conflicts.</div>{/if}
    </div>
  </aside>
{/if}
