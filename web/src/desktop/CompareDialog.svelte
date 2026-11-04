<script lang="ts">
  import { api } from "../lib/api";
  import { toast } from "../lib/stores";
  import type { AlignPair, AlignResult, AlignSection, Document } from "../lib/types";
  import Icon from "./Icon.svelte";
  import { docRef } from "./state.svelte";

  // Side-by-side document comparison (deterministic alignment, no LLM) —
  // same endpoint and semantics as the mobile Documents tab.
  let { docs, projectID, onClose }: { docs: Document[]; projectID: number; onClose: () => void } = $props();

  let a = $state("");
  let b = $state("");
  let mode = $state("versions");
  let busy = $state(false);
  let res = $state<AlignResult | null>(null);

  async function compare() {
    if (!a || !b || a === b) return;
    busy = true;
    res = null;
    try {
      res = await api.alignDocuments(a, b, mode, 0, projectID);
    } catch (e) {
      toast((e as Error).message, "error");
    } finally {
      busy = false;
    }
  }
  function label(p: AlignPair): string {
    return p.kind === "changed" ? "changed" : p.kind === "matched" ? "matched" : p.kind === "only_a" ? "only in A" : "only in B";
  }
  function where(secs: AlignSection[]): string {
    if (!secs.length) return "";
    return secs[0].page ? `p.${secs[0].page}` : `§${secs[0].ord + 1}`;
  }
  function count(kind: string): number {
    return res?.pairs.filter((p) => p.kind === kind).length ?? 0;
  }
</script>

<div class="modal-scrim" role="presentation" onclick={onClose}></div>
<div class="modal cmp" role="dialog" aria-label="Compare documents">
  <header><Icon name="columns" /><strong>Compare documents</strong><span class="spacer"></span>
    <button class="icon-btn" onclick={onClose} aria-label="Close"><Icon name="x" /></button></header>
  <div class="modal-body">
    <div class="row" style="flex-wrap:wrap;">
      <select bind:value={a} style="flex:1; min-width:200px;">
        <option value="">Document A…</option>
        {#each docs as d}<option value={docRef(d, projectID)}>{d.title} [{docRef(d, projectID)}]</option>{/each}
      </select>
      <select bind:value={b} style="flex:1; min-width:200px;">
        <option value="">Document B…</option>
        {#each docs as d}<option value={docRef(d, projectID)}>{d.title} [{docRef(d, projectID)}]</option>{/each}
      </select>
      <select bind:value={mode} title="versions: two revisions of one text; topical: different texts on one subject">
        <option value="versions">Versions of one text</option>
        <option value="topical">Two texts, same topic</option>
      </select>
      <button class="primary" onclick={compare} disabled={busy || !a || !b || a === b}>{busy ? "Aligning…" : "Compare"}</button>
    </div>
    {#if res}
      <div class="muted small">
        <strong>{res.a.title}</strong> (A, {res.a.sections} sections) vs <strong>{res.b.title}</strong> (B, {res.b.sections} sections)
        {#if res.mode === "versions"} · {res.identical} identical sections hidden{/if}
        · {count("changed") + count("matched")} paired · {count("only_a")} only in A · {count("only_b")} only in B
      </div>
      {#if res.pairs.length === 0}<div class="muted small">No differences — identical section for section.</div>{/if}
      {#each res.pairs as p}
        <div class="pair kind-{p.kind}">
          <div class="row small" style="flex-wrap:wrap;">
            <span class="pill">{label(p)}</span>
            {#if p.a_heading || p.b_heading}<b>{p.a_heading && p.b_heading && p.a_heading !== p.b_heading ? `${p.a_heading} ↔ ${p.b_heading}` : p.a_heading || p.b_heading}</b>{/if}
            {#if p.a.length && p.b.length}<span class="faint">similarity {(p.similarity ?? 0).toFixed(2)}</span>{/if}
            {#if p.a.length}<span class="faint">A {where(p.a)}</span>{/if}
            {#if p.b.length}<span class="faint">B {where(p.b)}</span>{/if}
          </div>
          <div class="cols">
            <div class="cell">{#if p.a.length}{#each p.a as s}<p class="wrap">{s.text}</p>{/each}{:else}<span class="faint small">(not in A)</span>{/if}</div>
            <div class="cell">{#if p.b.length}{#each p.b as s}<p class="wrap">{s.text}</p>{/each}{:else}<span class="faint small">(not in B)</span>{/if}</div>
          </div>
        </div>
      {/each}
    {/if}
  </div>
</div>

<style>
  .cmp { width: min(1100px, 94vw); height: 86vh; }
  .pair { border-left: 3px solid var(--accent-dim); padding-left: 10px; }
  .pair.kind-changed { border-left-color: #e8b34b; }
  .pair.kind-only_a { border-left-color: #d0645f; }
  .pair.kind-only_b { border-left-color: #5f93d0; }
  .cols { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; margin-top: 4px; }
  .cell { min-width: 0; max-height: 20em; overflow-y: auto; font-size: 13px; }
  .cell p { margin: 0 0 6px; }
</style>
