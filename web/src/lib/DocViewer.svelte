<script lang="ts">
  // The rendered-document sidebar, mounted once at app level so any surface
  // (chat citations/docrefs, the Projects tab's View buttons) opens documents
  // identically. State + handlers live in docview.svelte.ts.
  import { renderMarkdown } from "./markdown";
  import { docView, closeDocView, openDocBlob, downloadDoc, onCiteClick, onDocrefClick, onCodeCopy } from "./docview.svelte";
</script>

{#if docView.current}
  <div class="scrim" role="presentation" onclick={closeDocView}></div>
  <aside class="sheet right docview">
    <header>
      <strong class="doctitle" title={docView.current.title}>{docView.current.title}</strong>
      <span class="spacer"></span>
      <button class="ghost small" title="Open the raw file in a new tab" onclick={() => docView.current && openDocBlob(docView.current.blob)}>Raw ↗</button>
      <button class="ghost small" title="Download the original markdown" onclick={downloadDoc}>Download ⬇</button>
      <button class="ghost" onclick={closeDocView}>Close</button>
    </header>
    <!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
    <div class="body md" onclick={(e) => { onCiteClick(e); onDocrefClick(e); onCodeCopy(e); }}>
      {@html renderMarkdown(docView.current.text)}
    </div>
  </aside>
{/if}

<style>
  .docview { width: min(760px, 94vw); }
  .docview .doctitle { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .docview header button { flex-shrink: 0; }
  .docview .body { word-break: break-word; }
  .docview .body :global(p) { margin: 0.4em 0; }
  .docview .body :global(pre) { white-space: pre; overflow-x: auto; }
  .docview .body :global(.codewrap) { position: relative; }
</style>
