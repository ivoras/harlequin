import { marked } from "marked";
import DOMPurify from "dompurify";

marked.setOptions({ gfm: true, breaks: true });

// Render assistant markdown to sanitized HTML. Links open in a new tab.
export function renderMarkdown(src: string): string {
  const html = marked.parse(src ?? "", { async: false }) as string;
  const clean = DOMPurify.sanitize(html, { ADD_ATTR: ["target", "rel"] });
  return clean;
}
