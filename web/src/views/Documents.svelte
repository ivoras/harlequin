<script lang="ts">
  import { api } from "../lib/api";
  import { user, activeProject, toast } from "../lib/stores";
  import { isElevated } from "../lib/types";
  import type { Document, SearchResult } from "../lib/types";

  let docs = $state<Document[]>([]);
  let q = $state("");
  let results = $state<SearchResult[]>([]);
  let title = $state("");
  let content = $state("");
  let uploading = $state(false);
  let fileEl: HTMLInputElement | undefined;

  async function upload(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;
    uploading = true;
    try {
      const d = await api.uploadDocument(file);
      toast(`ingested "${d.title}"`);
      await load();
    } catch (err) {
      toast((err as Error).message, "error");
    } finally {
      uploading = false;
      if (fileEl) fileEl.value = ""; // allow re-selecting the same file
    }
  }

  // All currently visible corpora: personal + shared, plus the active
  // project's documents when one is set. Each row is scope-tagged.
  async function load() {
    try {
      docs = await api.listDocuments($activeProject?.id ?? 0);
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  // $effect runs on mount and whenever the active project changes.
  $effect(() => {
    void $activeProject;
    load();
  });
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
  function canDelete(d: Document): boolean {
    if (d.scope === "personal") return true; // your own corpus
    if (d.scope === "project") return true; // any member (you can see it)
    return isElevated($user?.role); // shared: owner/admin
  }
  async function del(d: Document) {
    try {
      await api.deleteDocument(d.id, d.scope ?? "", d.scope === "project" ? ($activeProject?.id ?? 0) : 0);
      docs = docs.filter((x) => x !== d);
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
      <div class="list">{#each results as r}<div class="card small">
        {#if r.scope || r.source}<div class="muted" style="font-size:0.82em; margin-bottom:6px;">{#if r.scope}[{r.scope}] {/if}{r.source ?? ""}</div>{/if}
        <div class="wrap">{r.content}</div>
      </div>{/each}</div>
    {/if}
    <h3 class="muted small">Library</h3>
    <div class="list">
      {#each docs as d}
        <div class="card row">
          <div class="col" style="flex:1; min-width:0; gap:1px;">
            <div class="row" style="gap:6px;">
              <strong>{d.title}</strong>
              <span class="pill scope-{d.scope}">{d.scope === "project" ? `project: ${$activeProject?.name ?? "?"}` : (d.scope ?? "shared")}</span>
            </div>
            <span class="muted small">{d.mime} · {new Date(d.created_at).toLocaleDateString()}</span>
          </div>
          {#if canDelete(d)}<button class="ghost danger small" onclick={() => del(d)} aria-label="Delete">✕</button>{/if}
        </div>
      {/each}
      {#if docs.length === 0}<div class="muted small">Empty.</div>{/if}
    </div>
    <div class="card col">
      <div class="row" style="align-items:center; gap:8px;">
        <input type="file" accept=".pdf,.txt,.md,application/pdf,text/plain" bind:this={fileEl} onchange={upload} disabled={uploading} />
        {#if uploading}<span class="muted small">extracting & ingesting…</span>{/if}
      </div>
      <div class="muted small">Upload a PDF or text file (the server extracts the text).</div>
      <hr />
      <input placeholder="title" bind:value={title} />
      <textarea rows="3" placeholder="…or paste text to ingest" bind:value={content}></textarea>
      <button class="primary" onclick={add} disabled={!content.trim()}>Ingest text</button>
    </div>
  </div>
</section>
