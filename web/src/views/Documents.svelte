<script lang="ts">
  import { api } from "../lib/api";
  import { user, activeProject, toast } from "../lib/stores";
  import { isElevated } from "../lib/types";
  import type { AlignPair, AlignResult, AlignSection, Document, SearchResult } from "../lib/types";

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

  // --- View a document's content: PDF/DOCX open the stored file in a new tab
  // (the browser/OS handles rendering); everything else (TXT — plain text or
  // markdown, including save_doc reports, which have no stored file at all)
  // expands inline from its chunk content. ---
  const FILE_MIME = new Set([
    "application/pdf",
    "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  ]);
  let openTextDoc = $state<Document | null>(null);
  let textContent = $state("");
  let loadingText = $state(false);

  function docProjectID(d: Document): number {
    return d.scope === "project" ? ($activeProject?.id ?? 0) : 0;
  }
  async function viewDoc(d: Document) {
    if (FILE_MIME.has(d.mime)) {
      try {
        const blob = await api.fetchDocumentFile(d.id, d.scope ?? "", docProjectID(d));
        const url = URL.createObjectURL(blob);
        window.open(url, "_blank", "noopener");
        setTimeout(() => URL.revokeObjectURL(url), 60_000);
      } catch (e) {
        toast((e as Error).message, "error");
      }
      return;
    }
    // TXT: toggle the inline panel closed if it's already open for this doc.
    if (openTextDoc === d) {
      openTextDoc = null;
      return;
    }
    openTextDoc = d;
    textContent = "";
    loadingText = true;
    try {
      const res = await api.getDocumentContent(d.id, d.scope ?? "", docProjectID(d));
      textContent = res.content;
    } catch (e) {
      toast((e as Error).message, "error");
      openTextDoc = null;
    } finally {
      loadingText = false;
    }
  }

  // --- Side-by-side comparison (deterministic alignment, no LLM) ---
  let cmpA = $state("");
  let cmpB = $state("");
  let cmpMode = $state("versions");
  let comparing = $state(false);
  let cmp = $state<AlignResult | null>(null);

  function refOf(d: Document): string {
    const letter = d.scope === "personal" ? "u" : d.scope === "project" ? "p" : "s";
    return `${letter}.${d.id}`;
  }
  async function compare() {
    if (!cmpA || !cmpB || cmpA === cmpB) return;
    comparing = true;
    cmp = null;
    try {
      cmp = await api.alignDocuments(cmpA, cmpB, cmpMode, 0, $activeProject?.id ?? 0);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      comparing = false;
    }
  }
  function pairLabel(p: AlignPair): string {
    switch (p.kind) {
      case "changed": return "changed";
      case "matched": return "matched";
      case "only_a": return "only in A";
      default: return "only in B";
    }
  }
  function whereOf(secs: AlignSection[]): string {
    if (!secs.length) return "";
    const s = secs[0];
    return s.page ? `p.${s.page}` : `§${s.ord + 1}`;
  }
  function kindCount(kind: string): number {
    return cmp?.pairs.filter((p) => p.kind === kind).length ?? 0;
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
        <div class="card col">
          <div class="row">
            <div class="col" style="flex:1; min-width:0; gap:1px;">
              <div class="row" style="gap:6px;">
                <strong>{d.title}</strong>
                <span class="pill scope-{d.scope}">{d.scope === "project" ? `project: ${$activeProject?.name ?? "?"}` : (d.scope ?? "shared")}</span>
              </div>
              <span class="muted small">{d.mime} · {new Date(d.created_at).toLocaleDateString()}</span>
              {#if d.description}<span class="muted small wrap">{d.description}</span>{/if}
            </div>
            <button class="ghost small" onclick={() => viewDoc(d)}>{openTextDoc === d ? "Hide" : "View"}</button>
            {#if canDelete(d)}<button class="ghost danger small" onclick={() => del(d)} aria-label="Delete">✕</button>{/if}
          </div>
          {#if openTextDoc === d}
            <div class="doc-text-panel">
              {#if loadingText}<span class="muted small">loading…</span>
              {:else}<pre class="wrap">{textContent}</pre>{/if}
            </div>
          {/if}
        </div>
      {/each}
      {#if docs.length === 0}<div class="muted small">Empty.</div>{/if}
    </div>
    <h3 class="muted small">Compare</h3>
    <div class="card col">
      <div class="row cmp-controls">
        <select bind:value={cmpA}>
          <option value="">document A…</option>
          {#each docs as d}<option value={refOf(d)}>{d.title} [{refOf(d)}]</option>{/each}
        </select>
        <select bind:value={cmpB}>
          <option value="">document B…</option>
          {#each docs as d}<option value={refOf(d)}>{d.title} [{refOf(d)}]</option>{/each}
        </select>
        <select bind:value={cmpMode} title="versions: two revisions of the same text; topics: different texts on the same subject">
          <option value="versions">versions of one text</option>
          <option value="topical">two texts, same topic</option>
        </select>
        <button class="primary small" onclick={compare} disabled={comparing || !cmpA || !cmpB || cmpA === cmpB}>
          {comparing ? "aligning…" : "Compare"}
        </button>
      </div>
      {#if cmp}
        <div class="muted small">
          <strong>{cmp.a.title}</strong> (A, {cmp.a.sections} sections) vs <strong>{cmp.b.title}</strong> (B, {cmp.b.sections} sections)
          {#if cmp.mode === "versions"} · {cmp.identical} identical sections hidden{/if}
          · {kindCount("changed") + kindCount("matched")} paired · {kindCount("only_a")} only in A · {kindCount("only_b")} only in B
        </div>
        {#if cmp.pairs.length === 0}
          <div class="muted small">No differences — the documents are identical section for section.</div>
        {/if}
        <div class="cmp-pairs">
          {#each cmp.pairs as p}
            <div class="cmp-pair kind-{p.kind}">
              <div class="cmp-meta">
                <span class="pill">{pairLabel(p)}</span>
                {#if p.a_heading || p.b_heading}
                  <span class="small cmp-heading">{p.a_heading && p.b_heading && p.a_heading !== p.b_heading ? `${p.a_heading} ↔ ${p.b_heading}` : (p.a_heading || p.b_heading)}</span>
                {/if}
                {#if p.a.length && p.b.length}<span class="muted small">similarity {(p.similarity ?? 0).toFixed(2)}</span>{/if}
                {#if p.a.length}<span class="muted small">A {whereOf(p.a)}</span>{/if}
                {#if p.b.length}<span class="muted small">B {whereOf(p.b)}</span>{/if}
              </div>
              <div class="cmp-cols">
                <div class="cmp-cell">
                  {#if p.a.length}{#each p.a as s}<p class="wrap">{s.text}</p>{/each}{:else}<span class="muted small">(not present in A)</span>{/if}
                </div>
                <div class="cmp-cell">
                  {#if p.b.length}{#each p.b as s}<p class="wrap">{s.text}</p>{/each}{:else}<span class="muted small">(not present in B)</span>{/if}
                </div>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>
    <div class="card col">
      <div class="row" style="align-items:center; gap:8px;">
        <input type="file" accept=".pdf,.docx,.txt,.md,application/pdf,text/plain" bind:this={fileEl} onchange={upload} disabled={uploading} />
        {#if uploading}<span class="muted small">extracting & ingesting…</span>{/if}
      </div>
      <div class="muted small">Upload a PDF, DOCX or text file (the server extracts the text).</div>
      <hr />
      <input placeholder="title" bind:value={title} />
      <textarea rows="3" placeholder="…or paste text to ingest" bind:value={content}></textarea>
      <button class="primary" onclick={add} disabled={!content.trim()}>Ingest text</button>
    </div>
  </div>
</section>

<style>
  .cmp-controls { flex-wrap: wrap; gap: 6px; }
  .cmp-controls select { flex: 1; min-width: 140px; }
  .cmp-pairs { display: flex; flex-direction: column; gap: 10px; }
  .cmp-pair { border-left: 3px solid var(--accent-dim); padding-left: 8px; }
  .cmp-pair.kind-changed { border-left-color: #e8b34b; }
  .cmp-pair.kind-only_a { border-left-color: #d0645f; }
  .cmp-pair.kind-only_b { border-left-color: #5f93d0; }
  .cmp-meta { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; flex-wrap: wrap; }
  .cmp-heading { font-weight: 600; }
  .doc-text-panel { max-height: 24em; overflow-y: auto; border-top: 1px solid var(--accent-dim); padding-top: 8px; margin-top: 4px; }
  .doc-text-panel pre { white-space: pre-wrap; font-family: inherit; margin: 0; font-size: 0.9em; }
  .cmp-cols { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
  .cmp-cell { min-width: 0; max-height: 16em; overflow-y: auto; font-size: 0.9em; }
  .cmp-cell p { margin: 0 0 6px; white-space: pre-wrap; }
  @media (max-width: 640px) {
    .cmp-cols { grid-template-columns: 1fr; }
  }
</style>
