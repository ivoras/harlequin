import { marked } from "marked";
import DOMPurify from "dompurify";

marked.setOptions({ gfm: true, breaks: true });

// Document-chunk citation ids the agent emits after search_docs results.
const CITE_RE = /\bd\.[usp]\.\d+\b/g;

// wrapCitations walks text nodes and wraps chunk ids (d.u.939 …) in
// <span class="cite" data-cite="…"> so the chat view can attach a tooltip and
// an open-the-document action. Code/links are left alone.
function wrapCitations(html: string): string {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  const walker = document.createTreeWalker(tpl.content, NodeFilter.SHOW_TEXT);
  const targets: Text[] = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const t = n as Text;
    if (t.parentElement?.closest("code, pre, a")) continue;
    if (CITE_RE.test(t.data)) targets.push(t);
    CITE_RE.lastIndex = 0;
  }
  for (const t of targets) {
    const frag = document.createDocumentFragment();
    let last = 0;
    for (const m of t.data.matchAll(CITE_RE)) {
      frag.append(t.data.slice(last, m.index));
      const span = document.createElement("span");
      span.className = "cite";
      span.dataset.cite = m[0];
      span.textContent = m[0];
      frag.append(span);
      last = (m.index ?? 0) + m[0].length;
    }
    frag.append(t.data.slice(last));
    t.replaceWith(frag);
  }
  return tpl.innerHTML;
}

// Bare scoped document ids the agent emits when referencing a whole document
// rather than one chunk (e.g. "saved as document p.18", "(p.18)"), distinct
// from the d.<scope>.<n> chunk citations above. Runs after wrapCitations, so
// a chunk citation's "u.939" is already inside a .cite span (skipped below)
// by the time this pass walks text nodes — it only catches genuinely bare refs.
const DOCREF_RE = /\b([usp])\.(\d+)\b/g;

// wrapDocRefs mirrors wrapCitations for whole-document references: wraps each
// in <span class="docref" data-docref="…"> so the chat view can open it.
function wrapDocRefs(html: string): string {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  const walker = document.createTreeWalker(tpl.content, NodeFilter.SHOW_TEXT);
  const targets: Text[] = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const t = n as Text;
    if (t.parentElement?.closest("code, pre, a, .cite")) continue;
    if (DOCREF_RE.test(t.data)) targets.push(t);
    DOCREF_RE.lastIndex = 0;
  }
  for (const t of targets) {
    const frag = document.createDocumentFragment();
    let last = 0;
    for (const m of t.data.matchAll(DOCREF_RE)) {
      frag.append(t.data.slice(last, m.index));
      const span = document.createElement("span");
      span.className = "docref";
      span.dataset.docref = m[0];
      span.textContent = m[0];
      frag.append(span);
      last = (m.index ?? 0) + m[0].length;
    }
    frag.append(t.data.slice(last));
    t.replaceWith(frag);
  }
  return tpl.innerHTML;
}

// Render assistant markdown to sanitized HTML. Links open in a new tab.
export function renderMarkdown(src: string): string {
  const html = marked.parse(src ?? "", { async: false }) as string;
  const clean = DOMPurify.sanitize(html, { ADD_ATTR: ["target", "rel"] });
  return wrapDocRefs(wrapCitations(clean));
}
