<script lang="ts">
  import { viewDocument } from "../lib/docview.svelte";
  import type { Document } from "../lib/types";
  import Icon from "./Icon.svelte";
  import { docRef, mimeLabel } from "./state.svelte";

  // A document listing. projectID resolves project-scope rows (refs, viewing);
  // canDelete/onDelete are supplied by the owning view.
  let {
    docs,
    projectID,
    projectName = "",
    showScope = true,
    canDelete,
    onDelete,
  }: {
    docs: Document[];
    projectID: number;
    projectName?: string;
    showScope?: boolean;
    canDelete: (d: Document) => boolean;
    onDelete: (d: Document) => void;
  } = $props();

  // The row whose "…" menu is open (scope:id), if any.
  let menuFor = $state<string | null>(null);
  const keyOf = (d: Document) => `${d.scope}:${d.id}`;

</script>

<div class="ftwrap">
<table class="ftable">
  <thead>
    <tr>
      <th>Name</th>
      {#if showScope}<th>Scope</th>{/if}
      <th class="c-ref">Ref</th>
      <th class="c-type">Type</th>
      <th class="num c-chunks">Chunks</th>
      <th class="c-added">Added</th>
      <th></th>
    </tr>
  </thead>
  <tbody>
    {#each docs as d (`${d.scope}:${d.id}`)}
      <tr ondblclick={() => viewDocument(d, d.scope === "project" ? projectID : 0)}>
        <td class="name">
          <button class="link" onclick={() => viewDocument(d, d.scope === "project" ? projectID : 0)}>
            <Icon name="file" size={14} /> <span class="ellipsize">{d.title}</span>
          </button>
          {#if d.description}<div class="desc faint">{d.description}</div>{/if}
        </td>
        {#if showScope}<td><span class="pill scope-{d.scope}" title={d.scope === "project" ? projectName : ""}>{d.scope ?? "shared"}</span></td>{/if}
        <td class="c-ref"><code class="ref">{docRef(d, projectID)}</code></td>
        <td class="faint c-type">{mimeLabel(d.mime)}</td>
        <td class="num faint c-chunks">{d.chunks ?? ""}</td>
        <td class="faint nowrap c-added">{new Date(d.created_at).toLocaleDateString()}</td>
        <td class="acts">
          <button class="icon-btn" title="View" aria-label="View" onclick={() => viewDocument(d, d.scope === "project" ? projectID : 0)}><Icon name="external" size={14} /></button>
          <span class="menu-wrap">
            <button class="icon-btn" title="More" aria-label="More actions for {d.title}" aria-haspopup="menu"
              aria-expanded={menuFor === keyOf(d)} onclick={() => (menuFor = menuFor === keyOf(d) ? null : keyOf(d))}><Icon name="more" size={14} /></button>
            {#if menuFor === keyOf(d)}
              <div class="menu-click-away" role="presentation" onclick={() => (menuFor = null)}></div>
              <div class="popover row-pop" role="menu">
                <button class="menu-item del" role="menuitem" disabled={!canDelete(d)}
                  title={canDelete(d) ? "Permanently delete this file, its Markdown text and its search index entries" : "Only owners and admins can delete shared files"}
                  onclick={() => { menuFor = null; onDelete(d); }}><Icon name="trash" size={14} /><span class="grow">Delete…</span></button>
              </div>
            {/if}
          </span>
        </td>
      </tr>
    {/each}
  </tbody>
</table>
</div>

<style>
  /* Drop secondary columns as the table narrows (side panel open, small window). */
  .ftwrap { container-type: inline-size; }
  @container (max-width: 820px) { .c-type, .c-chunks { display: none; } }
  @container (max-width: 560px) { .c-ref, .c-added { display: none; } }
  .ftable { width: 100%; border-collapse: collapse; font-size: 13px; table-layout: auto; }
  th { text-align: left; font-weight: 600; font-size: 11.5px; color: var(--faint); text-transform: uppercase; letter-spacing: 0.04em;
    padding: 6px 10px; border-bottom: 1px solid var(--border); white-space: nowrap; }
  /* Every column but Name shrinks to its content; Name takes the rest and truncates. */
  td { padding: 6px 10px; border-bottom: 1px solid var(--border-soft); vertical-align: top; width: 1%; white-space: nowrap; }
  tr:hover td { background: var(--hover); }
  .num { text-align: right; }
  .nowrap { white-space: nowrap; }
  .name { width: 100%; max-width: 0; }
  .link { background: none; border: none; box-shadow: none; padding: 0; min-height: 0; gap: 7px; color: var(--text);
    font-weight: 500; max-width: 100%; justify-content: flex-start; }
  .link:hover { background: none; color: var(--accent-text); }
  .desc { font-size: 12px; margin: 2px 0 0 21px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ref { font-size: 11.5px; white-space: nowrap; }
  .acts { white-space: nowrap; text-align: right; }
  .acts .icon-btn { opacity: 0.5; }
  tr:hover .acts .icon-btn { opacity: 1; }
  .menu-wrap { position: relative; display: inline-block; }
  .row-pop { top: calc(100% + 2px); right: 0; min-width: 150px; text-align: left; }
  .menu-item.del:not(:disabled) { color: var(--danger); }
</style>
