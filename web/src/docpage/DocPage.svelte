<script lang="ts">
  // A document as a standalone, printable page at /doc/<ref> (u.N | s.N | p<id>.N,
  // see docPageURL). The HTML is the same static shell for everyone; the content
  // is fetched through the API with the viewer's own login, so only users who
  // can see the document get it.
  import { api, getToken } from "../lib/api";
  import { renderMarkdown } from "../lib/markdown";

  const SCOPES: Record<string, string> = { u: "personal", s: "shared", p: "project" };
  const m = /^\/doc\/([usp])(\d*)\.(\d+)\/?$/.exec(location.pathname);
  const scope = m ? SCOPES[m[1]] : "";
  const projectID = m && m[1] === "p" ? Number(m[2]) : 0;
  const id = m ? Number(m[3]) : 0;
  const valid = !!m && !(m[1] === "p" && !projectID);

  let title = $state("");
  let html = $state("");
  let error = $state("");
  let signin = $state(false);

  async function load() {
    if (!valid) {
      error = "Not a document address. Expected /doc/u.12, /doc/s.3 or /doc/p3.17.";
      return;
    }
    if (!getToken()) {
      signin = true;
      return;
    }
    try {
      const docs = await api.listDocuments(projectID);
      const d = docs.find((x) => x.id === id && (x.scope ?? "personal") === scope);
      if (!d) throw new Error("not found");
      title = d.title;
      document.title = d.title + " — Harlequin";
      let text: string;
      try {
        const f = await api.fetchDocumentFile(id, scope, projectID);
        if (!f.blob.type.startsWith("text/")) throw new Error("not text");
        text = (await f.blob.text()).replace(/^﻿/, "");
      } catch {
        text = (await api.getDocumentContent(id, scope, projectID)).content; // PDF/DOCX: extracted text
      }
      html = renderMarkdown(text);
    } catch (e) {
      const msg = (e as Error).message;
      if (/^401/.test(msg)) signin = true;
      else error = "This document doesn't exist, or you don't have access to it.";
    }
  }
  load();
</script>

<main>
  {#if signin}
    <p class="note">Sign in to view this document, then reload this page.</p>
    <p class="note"><a href="/">Open Harlequin</a></p>
  {:else if error}
    <p class="note">{error}</p>
  {:else if !html}
    <p class="note">Loading…</p>
  {:else}
    <header>
      <span class="scope">{scope === "project" ? "Project " + projectID : scope}</span>
      <h1 class="doctitle">{title}</h1>
      <button class="print" onclick={() => window.print()}>Print</button>
    </header>
    <article class="md">{@html html}</article>
  {/if}
</main>

<style>
  :global(:root) {
    --bg: #fbfaf7; --fg: #1d1a24; --muted: #6d6678; --border: #ddd6ea; --surface-2: #f1edf7; --accent: #6b4fbb;
    --maxw: 46rem;
  }
  @media (prefers-color-scheme: dark) {
    :global(:root) { --bg: #16121f; --fg: #e8e4f0; --muted: #a39cb3; --border: #362e4f; --surface-2: #262039; --accent: #a78bfa; }
  }
  :global(body) {
    margin: 0; background: var(--bg); color: var(--fg);
    font: 17px/1.7 Georgia, "Iowan Old Style", "Times New Roman", serif;
  }
  main { max-width: var(--maxw); margin: 0 auto; padding: 48px 20px 80px; }
  header { margin-bottom: 28px; padding-bottom: 16px; border-bottom: 1px solid var(--border); position: relative; }
  .doctitle { font-size: 2rem; line-height: 1.25; margin: 4px 0 0; padding-right: 64px; }
  .scope { font: 600 12px/1 system-ui, sans-serif; letter-spacing: 0.08em; text-transform: uppercase; color: var(--muted); }
  .print { position: absolute; top: 0; right: 0; font: 13px system-ui, sans-serif; color: var(--muted);
    background: none; border: 1px solid var(--border); border-radius: 6px; padding: 4px 10px; cursor: pointer; }
  .note { font-family: system-ui, sans-serif; color: var(--muted); }
  a { color: var(--accent); }
  article :global(h1), article :global(h2), article :global(h3), article :global(h4) { line-height: 1.3; margin: 1.6em 0 0.5em; }
  article :global(p), article :global(li) { margin: 0.7em 0; }
  article :global(blockquote) { margin: 1em 0; padding: 0 1em; border-left: 3px solid var(--border); color: var(--muted); }
  article :global(pre) { background: var(--surface-2); padding: 12px 14px; border-radius: 8px; overflow-x: auto; font-size: 0.85em; line-height: 1.5; }
  article :global(code) { font-size: 0.9em; }
  article :global(table) { font: 15px/1.5 system-ui, sans-serif; }
  article :global(img) { max-width: 100%; }
  article :global(.codecopy) { display: none; }
  @media print {
    :global(body) { background: #fff; color: #000; font-size: 12pt; }
    main { max-width: none; padding: 0; }
    .print { display: none; }
  }
</style>
