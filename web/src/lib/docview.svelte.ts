// Shared document-viewing state + handlers: the rendered-markdown sidebar
// (DocViewer.svelte), blob-tab opening, and the citation/docref/code-copy
// click delegation used by the chat transcript, the rendered reports inside
// the viewer itself, and the Projects tab's View buttons. Extracted from
// Chat.svelte so every surface opens documents identically.
import { api } from "./api";
import { sc } from "./session.svelte";
import { toast } from "./stores";
import type { DocChunkInfo, Document } from "./types";

// --- The viewer's state (null = closed) ---
// DocRef identifies a document unambiguously (see docPageURL).
export type DocRef = { scope: string; id: number; projectID: number };

export const docView = $state<{
  current: { title: string; text: string; blob: Blob; filename: string; ref?: DocRef } | null;
  focus: string; // chunk text to scroll to once rendered ("" = none)
}>({ current: null, focus: "" });

export function closeDocView() {
  docView.current = null;
  docView.focus = "";
}

// openDocBlob opens a fetched document in a new tab via a blob: URL. Text
// blobs get a UTF-8 BOM prepended: browsers ignore the charset parameter of
// a blob's MIME type when navigating to it and fall back to windows-1252, so
// the BOM is the only encoding signal that reliably survives.
export function openDocBlob(blob: Blob, anchor = "") {
  const b = blob.type.startsWith("text/") ? new Blob(["\uFEFF", blob], { type: blob.type }) : blob;
  const url = URL.createObjectURL(b);
  window.open(url + anchor, "_blank", "noopener");
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

// showTextDoc opens the sidebar on a text blob (markdown rendered by the
// viewer component).
export async function showTextDoc(blob: Blob, title: string, filename: string, ref?: DocRef) {
  // (focus is set by the caller before this, and cleared by closeDocView)
  const text = (await blob.text()).replace(/^\uFEFF/, "");
  const name = filename || title || "document";
  docView.current = { title: title || filename || "document", text, blob, filename: /\.\w+$/.test(name) ? name : name + ".md", ref };
}

// openDoc routes a fetched document: text into the sidebar, the rest into a tab.
export async function openDoc(file: { blob: Blob; filename: string }, title: string, anchor = "", ref?: DocRef) {
  if (file.blob.type.startsWith("text/")) await showTextDoc(file.blob, title, file.filename, ref);
  else openDocBlob(file.blob, anchor);
}

export function downloadDoc() {
  const d = docView.current;
  if (!d) return;
  const url = URL.createObjectURL(d.blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = d.filename;
  a.click();
  URL.revokeObjectURL(url);
}

// docPageURL is the document's own page: /doc/u.12 (personal), /doc/s.3
// (shared), /doc/p3.17 (project 3, document 17) — the same notation as chat
// references, with the project always spelled out so the URL means the same
// thing everywhere. Personal ids are per user, so a /doc/u.N link only opens
// for its owner. The page fetches through the API with the viewer's own login.
export function docPageURL(r: DocRef): string {
  const s = r.scope === "project" ? "p" + r.projectID : r.scope === "shared" ? "s" : "u";
  return `/doc/${s}.${r.id}`;
}

// openDocPage shows the open document as a standalone page in a new tab.
export function openDocPage() {
  const r = docView.current?.ref;
  if (r) window.open(docPageURL(r), "_blank", "noopener");
}

// viewDocument opens a document row (e.g. the Projects tab's View button)
// exactly like clicking its reference in chat: stored original first (text →
// sidebar, PDF/DOCX → tab), extracted-text fallback for documents with no
// stored file.
export async function viewDocument(d: Document, projectID: number) {
  const scope = d.scope ?? "";
  try {
    const ref = { scope, id: d.id, projectID };
    const file = await api.fetchDocumentFile(d.id, scope, projectID);
    await openDoc(file, d.title, "", ref);
  } catch {
    try {
      const res = await api.getDocumentContent(d.id, scope, projectID);
      await showTextDoc(new Blob([res.content], { type: "text/markdown;charset=utf-8" }), d.title, "", { scope, id: d.id, projectID });
    } catch (err) {
      toast((err as Error).message || `couldn't open "${d.title}"`, "error");
    }
  }
}

// focusCited is a Svelte action for the element holding the rendered document:
// it scrolls to (and flashes) the block containing the cited chunk. Chunk text
// is raw markdown and the view is rendered HTML, so compare letters+digits only.
const normText = (s: string) => s.toLowerCase().replace(/[^\p{L}\p{N}]+/gu, "");
export function focusCited(el: HTMLElement, _dep?: unknown) {
  function run() {
    const focus = docView.focus;
    if (!focus || !docView.current) return;
    docView.focus = "";
    const want = normText(focus).slice(0, 60);
    if (want.length < 12) return;
    queueMicrotask(() => {
      for (const b of el.querySelectorAll<HTMLElement>("p, li, h1, h2, h3, h4, h5, h6, pre, td, blockquote")) {
        const have = normText(b.textContent ?? "");
        if (have.includes(want) || (have.length >= 12 && want.startsWith(have.slice(0, 60)))) {
          b.scrollIntoView({ block: "start" });
          b.classList.add("cite-focus");
          setTimeout(() => b.classList.remove("cite-focus"), 2500);
          return;
        }
      }
    });
  }
  run();
  return { update: run };
}

// --- Document citations (d.u.N spans produced by renderMarkdown) ---
// Lazily resolved and cached; hover sets a tooltip (title + scope + page),
// click opens the document (text → sidebar, PDFs anchored to the page → tab).
const citeCache = new Map<string, Promise<DocChunkInfo>>();
function resolveCite(cid: string): Promise<DocChunkInfo> {
  let p = citeCache.get(cid);
  if (!p) {
    // A bare d.p.N resolves against the session's project; a qualified
    // d.p<id>.N carries its project in the reference itself.
    p = api.getDocChunk(cid, cid.startsWith("d.p.") ? sc.currentProjectID : 0);
    citeCache.set(cid, p);
  }
  return p;
}
function citeTarget(e: Event): HTMLElement | null {
  const el = (e.target as HTMLElement)?.closest?.(".cite");
  return el instanceof HTMLElement ? el : null;
}

export async function onCiteHover(e: Event) {
  const el = citeTarget(e);
  const cid = el?.dataset.cite;
  if (!el || !cid || el.title) return;
  try {
    const info = await resolveCite(cid);
    const page = info.page ? `, p.${info.page}` : "";
    const open = info.has_file ? " — click to open" : "";
    el.title = `${info.title || "untitled"} (${info.scope}${page})${open}`;
    if (info.has_file) el.classList.add("openable");
  } catch {
    el.title = "unknown reference";
  }
}

export async function onCiteClick(e: Event) {
  const el = citeTarget(e);
  const cid = el?.dataset.cite;
  if (!el || !cid) return;
  try {
    const info = await resolveCite(cid);
    if (!info.has_file) {
      toast(`${info.title || "untitled"} (${info.scope}) — no stored file to open`);
      return;
    }
    const projectID = info.scope === "project" ? info.project_id || sc.currentProjectID : 0;
    const file = await api.fetchDocumentFile(info.document_id, info.scope, projectID);
    const anchor = info.mime === "application/pdf" && info.page ? `#page=${info.page}` : "";
    docView.focus = info.text ?? "";
    await openDoc(file, info.title || "", anchor, { scope: info.scope, id: info.document_id, projectID });
  } catch (err) {
    toast((err as Error).message, "error");
  }
}

// --- Whole-document references (p.18, u.4 … spans produced by renderMarkdown) ---
// Unlike a chunk citation, a doc ref carries no page/chunk to resolve first —
// scope and id come straight from the ref text. Try the stored original
// (PDF/DOCX) first; documents with no stored file (e.g. legacy save_doc
// reports) fall back to their full extracted text.
const DOCREF_SCOPE: Record<string, string> = { u: "personal", p: "project", s: "shared" };
function docrefTarget(e: Event): HTMLElement | null {
  const el = (e.target as HTMLElement)?.closest?.(".docref");
  return el instanceof HTMLElement ? el : null;
}

export async function onDocrefClick(e: Event) {
  const el = docrefTarget(e);
  const ref = el?.dataset.docref;
  if (!el || !ref) return;
  const [letter, idStr] = ref.split(".");
  const id = Number(idStr);
  // p<id> is a qualified project ref (e.g. p3.17); bare p uses the session's
  // project.
  let scope = DOCREF_SCOPE[letter];
  let projectID = scope === "project" ? sc.currentProjectID : 0;
  if (!scope && /^p\d+$/.test(letter)) {
    scope = "project";
    projectID = Number(letter.slice(1));
  }
  if (!scope || !id) return;
  try {
    const dref = { scope, id, projectID };
    const file = await api.fetchDocumentFile(id, scope, projectID);
    await openDoc(file, ref, "", dref);
  } catch {
    try {
      const res = await api.getDocumentContent(id, scope, projectID);
      await showTextDoc(new Blob([res.content], { type: "text/markdown;charset=utf-8" }), ref, "", { scope, id, projectID });
    } catch (err) {
      toast((err as Error).message || `couldn't open ${ref}`, "error");
    }
  }
}

// Per-code-block copy: .codecopy buttons injected by renderMarkdown, handled
// via the same event delegation as citations/docrefs. Feedback is a brief ✓
// swap directly on the button (it lives in {@html} output, not the template).
export async function onCodeCopy(e: Event) {
  const btn = (e.target as HTMLElement)?.closest?.(".codecopy");
  if (!(btn instanceof HTMLElement)) return;
  const pre = btn.parentElement?.querySelector("pre");
  if (!pre) return;
  try {
    await navigator.clipboard.writeText(pre.textContent ?? "");
  } catch (err) {
    toast((err as Error).message || "copy failed", "error");
    return;
  }
  btn.textContent = "✓";
  setTimeout(() => (btn.textContent = "⧉"), 1200);
}
