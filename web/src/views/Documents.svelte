<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { user, toast } from "../lib/stores";
  import { isElevated } from "../lib/types";
  import type { Document, SearchResult } from "../lib/types";

  let docs = $state<Document[]>([]);
  let q = $state("");
  let results = $state<SearchResult[]>([]);
  let title = $state("");
  let content = $state("");

  async function load() {
    try {
      docs = await api.listDocuments();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  onMount(load);
  async function search() {
    if (!q.trim()) {
      results = [];
      return;
    }
    try {
      results = await api.searchDocuments(q.trim());
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function add() {
    if (!content.trim()) return;
    try {
      await api.createDocument({ title: title.trim() || "Untitled", uri: "", mime: "text/plain", content });
      title = "";
      content = "";
      toast("ingested");
      await load();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function del(id: number) {
    try {
      await api.deleteDocument(id);
      docs = docs.filter((d) => d.id !== id);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
</script>

<section class="panel">
  <div class="container col">
    <h3>Documents</h3>
    <div class="row">
      <input placeholder="search the corpus…" bind:value={q} onkeydown={(e) => e.key === "Enter" && search()} />
      <button class="small" onclick={search}>Search</button>
    </div>
    {#if results.length}
      <div class="list">{#each results as r}<div class="card small wrap">{r.content}</div>{/each}</div>
    {/if}
    <h3 class="muted small">Library</h3>
    <div class="list">
      {#each docs as d}
        <div class="card row">
          <div class="col" style="flex:1; min-width:0; gap:1px;">
            <strong>{d.title}</strong>
            <span class="muted small">{d.mime} · {new Date(d.created_at).toLocaleDateString()}</span>
          </div>
          {#if isElevated($user?.role)}<button class="ghost danger small" onclick={() => del(d.id)} aria-label="Delete">✕</button>{/if}
        </div>
      {/each}
      {#if docs.length === 0}<div class="muted small">Empty.</div>{/if}
    </div>
    <div class="card col">
      <input placeholder="title" bind:value={title} />
      <textarea rows="3" placeholder="paste text to ingest…" bind:value={content}></textarea>
      <button class="primary" onclick={add} disabled={!content.trim()}>Ingest</button>
    </div>
  </div>
</section>
