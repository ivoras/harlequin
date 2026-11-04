<script lang="ts">
  import { api } from "../lib/api";
  import { user, activeProject, toast } from "../lib/stores";
  import { isElevated, type Document, type SearchResult } from "../lib/types";
  import Icon from "./Icon.svelte";
  import TopBar from "./TopBar.svelte";
  import FileTable from "./FileTable.svelte";
  import CompareDialog from "./CompareDialog.svelte";
  import { ui, type UploadTarget } from "./state.svelte";

  // Every corpus visible in this workspace, filterable by scope, with upload
  // (button or drop) into a chosen destination.
  type Filter = "all" | "project" | "personal" | "shared";
  let filter = $state<Filter>("all");
  let nameFilter = $state("");
  let query = $state("");
  let results = $state<SearchResult[] | null>(null);
  let searching = $state(false);
  let showPaste = $state(false);
  let showCompare = $state(false);
  let pasteTitle = $state("");
  let pasteText = $state("");

  const projectID = $derived($activeProject?.id ?? 0);
  const elevated = $derived(isElevated($user?.role));

  // Destinations this user may add to; the first is the default (the project
  // when one is active — the workspace they're in — else personal).
  const targets = $derived.by<UploadTarget[]>(() => {
    const t: UploadTarget[] = [];
    if ($activeProject) t.push({ scope: "project", projectID: $activeProject.id, label: `project “${$activeProject.name}”` });
    t.push({ scope: "personal", projectID: 0, label: "your personal files" });
    if (elevated) t.push({ scope: "shared", projectID: 0, label: "shared (all users)" });
    return t;
  });
  let targetScope = $state<string>("");
  $effect(() => {
    if (!targets.some((t) => t.scope === targetScope)) targetScope = targets[0].scope;
  });
  const target = $derived(targets.find((t) => t.scope === targetScope) ?? targets[0]);
  // Drops on this page go to the destination picked here.
  $effect(() => {
    ui.dropTarget = target;
    return () => (ui.dropTarget = null);
  });
  $effect(() => {
    if (filter === "project" && !$activeProject) filter = "all";
  });

  $effect(() => {
    void projectID;
    ui.loadDocs();
  });

  const counts = $derived({
    all: ui.docs.length,
    project: ui.docs.filter((d) => d.scope === "project").length,
    personal: ui.docs.filter((d) => d.scope === "personal").length,
    shared: ui.docs.filter((d) => d.scope !== "project" && d.scope !== "personal").length,
  });
  const shown = $derived.by(() => {
    const n = nameFilter.trim().toLowerCase();
    return ui.docs.filter((d) => {
      const sc = d.scope === "project" || d.scope === "personal" ? d.scope : "shared";
      if (filter !== "all" && sc !== filter) return false;
      return !n || d.title.toLowerCase().includes(n) || (d.description ?? "").toLowerCase().includes(n);
    });
  });

  function canDelete(d: Document): boolean {
    if (d.scope === "personal" || d.scope === "project") return true;
    return elevated;
  }
  async function del(d: Document) {
    if (!confirm(`Delete "${d.title}"? This removes it from search for everyone who can see it.`)) return;
    try {
      await api.deleteDocument(d.id, d.scope ?? "", d.scope === "project" ? projectID : 0);
      ui.docsChanged();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
  async function search() {
    if (!query.trim()) {
      results = null;
      return;
    }
    searching = true;
    try {
      results = await api.searchDocuments(query.trim(), projectID);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      searching = false;
    }
  }
  function pick() {
    const inp = document.createElement("input");
    inp.type = "file";
    inp.multiple = true;
    inp.onchange = () => inp.files && ui.upload([...inp.files], target);
    inp.click();
  }
  async function ingestText() {
    if (!pasteText.trim()) return;
    try {
      await api.createDocument({
        title: pasteTitle.trim() || "Untitled",
        uri: "",
        mime: "text/plain",
        content: pasteText,
        scope: target.scope,
        project_id: target.scope === "project" ? target.projectID : undefined,
      });
      pasteTitle = "";
      pasteText = "";
      showPaste = false;
      toast("added to " + target.label);
      ui.docsChanged();
    } catch (e) {
      toast((e as Error).message, "error");
    }
  }
</script>

<TopBar label="Files" />

<div class="page">
  <div class="container col">
    <div class="head row">
      <div class="col title-col">
        <h2>Files</h2>
        <span class="muted small">Documents the agent can search and cite in this workspace.</span>
      </div>
      <button onclick={() => (showCompare = true)} disabled={ui.docs.length < 2}><Icon name="columns" size={14} /> Compare</button>
      <button onclick={() => (showPaste = true)}><Icon name="text" size={14} /> Paste text</button>
    </div>

    <div class="row toolbar">
      <div class="seg">
        <button class:active={filter === "all"} onclick={() => (filter = "all")}>All <span class="faint">{counts.all}</span></button>
        {#if $activeProject}<button class:active={filter === "project"} onclick={() => (filter = "project")}>Project <span class="faint">{counts.project}</span></button>{/if}
        <button class:active={filter === "personal"} onclick={() => (filter = "personal")}>Personal <span class="faint">{counts.personal}</span></button>
        <button class:active={filter === "shared"} onclick={() => (filter = "shared")}>Shared <span class="faint">{counts.shared}</span></button>
      </div>
      <input class="namefilter" placeholder="Filter by name" bind:value={nameFilter} />
      <span class="spacer"></span>
      <div class="upload-split">
        {#if targets.length > 1}
          <select bind:value={targetScope} title="Where new files go">
            {#each targets as t}<option value={t.scope}>to {t.label}</option>{/each}
          </select>
        {/if}
        <button class="primary" onclick={pick}><Icon name="upload" size={14} /> Upload</button>
      </div>
    </div>

    <div class="row">
      <div class="searchbox">
        <Icon name="search" size={14} />
        <input placeholder="Search inside documents…" bind:value={query} onkeydown={(e) => e.key === "Enter" && search()} />
        {#if results}<button class="icon-btn" onclick={() => { results = null; query = ""; }} aria-label="Clear search"><Icon name="x" size={13} /></button>{/if}
      </div>
    </div>

    {#if searching}<div class="faint small">Searching…</div>{/if}
    {#if results}
      <div class="results">
        <div class="faint small">{results.length} passage{results.length === 1 ? "" : "s"} matching “{query}”</div>
        {#each results as r}
          <div class="card small">
            {#if r.scope || r.source}<div class="faint" style="margin-bottom:4px;">{#if r.scope}<span class="pill scope-{r.scope}">{r.scope}</span> {/if}{r.source ?? ""}</div>{/if}
            <div class="wrap">{r.content}</div>
          </div>
        {/each}
      </div>
    {/if}

    {#if shown.length}
      <FileTable docs={shown} {projectID} projectName={$activeProject?.name ?? ""} {canDelete} onDelete={del} />
    {:else}
      <div class="dropzone" role="button" tabindex="0" onclick={pick} onkeydown={(e) => e.key === "Enter" && pick()}>
        <Icon name="upload" size={28} />
        <div><b>Drop files here</b> or click to upload</div>
        <div class="faint small">PDF, DOCX, Markdown or text · added to {target.label}</div>
      </div>
    {/if}
  </div>
</div>

{#if showPaste}
  <div class="modal-scrim" role="presentation" onclick={() => (showPaste = false)}></div>
  <div class="modal" role="dialog" aria-label="Paste text">
    <header><Icon name="text" /><strong>Add text as a document</strong><span class="spacer"></span>
      <button class="icon-btn" onclick={() => (showPaste = false)} aria-label="Close"><Icon name="x" /></button></header>
    <div class="modal-body">
      <input placeholder="Title" bind:value={pasteTitle} />
      <textarea rows="12" placeholder="Paste or type text…" bind:value={pasteText}></textarea>
      {#if targets.length > 1}
        <label class="row small muted">Add to
          <select bind:value={targetScope}>{#each targets as t}<option value={t.scope}>{t.label}</option>{/each}</select>
        </label>
      {:else}
        <div class="faint small">Added to {target.label}.</div>
      {/if}
    </div>
    <footer>
      <button class="ghost" onclick={() => (showPaste = false)}>Cancel</button>
      <button class="primary" onclick={ingestText} disabled={!pasteText.trim()}>Add document</button>
    </footer>
  </div>
{/if}

{#if showCompare}
  <CompareDialog docs={ui.docs} {projectID} onClose={() => (showCompare = false)} />
{/if}

<style>
  .page { flex: 1; min-height: 0; overflow-y: auto; }
  .page .container { max-width: 1180px; gap: 14px; }
  h2 { margin: 0; font-size: 20px; }
  .head { align-items: flex-end; }
  .title-col { gap: 0; flex: 1 1 280px; min-width: 0; }
  .upload-split { display: inline-flex; align-items: center; gap: 6px; }
  .toolbar { flex-wrap: wrap; }
  .seg { display: inline-flex; background: var(--surface-2); border-radius: 8px; padding: 2px; gap: 2px; }
  .seg button { background: transparent; border: none; box-shadow: none; min-height: 26px; padding: 2px 10px; font-size: 13px; color: var(--muted); }
  .seg button.active { background: var(--surface); color: var(--text); box-shadow: var(--shadow-1); }
  .namefilter { width: 200px; padding: 4px 9px; }
  .searchbox { display: flex; align-items: center; gap: 6px; border: 1px solid var(--border); border-radius: var(--radius-sm);
    padding: 0 6px 0 9px; background: var(--surface); flex: 1; color: var(--faint); }
  .searchbox:focus-within { border-color: var(--accent-dim); }
  .searchbox input { border: none; box-shadow: none; padding: 5px 0; background: transparent; }
  .results { display: flex; flex-direction: column; gap: 8px; }
  .dropzone { border: 2px dashed var(--border); border-radius: 14px; padding: 48px 20px; display: flex; flex-direction: column;
    align-items: center; gap: 6px; color: var(--muted); cursor: pointer; text-align: center; }
  .dropzone:hover { border-color: var(--accent-dim); color: var(--text); }
</style>
