---
name: create-word
description: Create a Word document (.docx) the user can download and edit — a report, letter, contract draft, summary, list or table written by you, or an existing text document turned into Word. load_skill this whenever the user asks for a Word document, a .docx, "as Word", or something they want to edit in Word. It explains the Markdown (and, for special layouts, HTML) the create_word tool converts.
---

# Create a Word document

You write the document as ordinary Markdown and the `create_word` tool converts
it into a real `.docx` (A4 pages) with Word's own styles — Heading 1/2/3, lists,
tables — saved in the user's files (the project's, in a project session). The
user opens it in Word and edits it; you do not design the page.

## The format

Same Markdown as `create_pdf`:

```markdown
# Service agreement

Between **Primjer d.o.o.** and the client.

## 1. Scope

- bookkeeping
- payroll

| Service | Price (EUR) |
|---|---:|
| Bookkeeping | 120,00 |
| Payroll | 45,50 |

\newpage

## 2. Terms

1. Monthly invoicing.
2. 30-day payment term.
```

Rules — follow them exactly:

1. **Start with one `# Title` line.** It is the printed title. The tool's
   `title` argument is only the file name.
2. **Use `##` and `###` for sections**, with an empty line around every
   heading, list, table and paragraph.
3. **Tables** are pipe tables: one line per row, same number of cells, a
   `|---|---|` separator line second; `---:` right-aligns numbers.
4. **New page:** `\newpage` alone on a line, with an empty line before and
   after.
5. **Wide tables** (more than about 6 columns): pass `"landscape": true`.
6. **Text styles:** `**bold**`, `*italic*`, `- ` and `1. ` lists, `> ` quotes,
   `---` rule, `[text](https://…)` links, fenced code blocks.
7. **Write amounts and dates as the user's language writes them**
   (`1.620,00 EUR`, `31.10.2026.`); they are kept as written. Compute amounts
   with `calculator` first.
8. **Not supported:** images from URLs or files, logos, headers/footers, page
   numbers, tracked changes, fields. Say so if the user asks.

## Where the text comes from

Give it in exactly one way:

| You have | Call |
|---|---|
| a short text (up to about two pages) | `"content": "<the Markdown>"` |
| a long text | build `tmp://report.md` with `edit_file` (`append`), then `"source": "tmp://report.md"` |
| a saved text document the user wants as Word | `"source": "u.12"` |

## HTML format (only for special layouts)

Use `"format": "html"` only for colours, alignment or spacing Markdown cannot
express. Put simple CSS in one `<style>` element (colours, font sizes,
`text-align`, margins, table borders); LibreOffice reads basic CSS, not
columns or flexbox. `<div class="pagebreak"></div>` starts a new page. Scripts,
forms and everything loaded from elsewhere are removed, and the tool reports
how many items it removed. Do not write `<html>`, `<head>` or `<body>`.

## Workflow

1. Gather and check the content first (`search_docs`, `read_doc`,
   `calculator`).
2. Call `create_word({"title": "Service agreement", "content": "<the Markdown>"})`,
   or build long text in the sandbox first and pass `source`.
3. Tell the user the document was created (its id, e.g. `u.42`, the file name)
   and that they can open or download it from their files. Repeat any note the
   tool returned (e.g. images left out).

To change a Word document you created, use `edit_doc` (load_skill
"edit-document"): it edits the Markdown the file was made from and rebuilds the
file, keeping the document's id. An **uploaded** .docx is also changed in place
by `edit_doc`, with its formatting kept. `create_word` itself always makes a new
file: use it to turn another document's text into a Word file
(`"source": "u.12"` uses the extracted text; the original layout is not kept).

If it says Word creation is not set up on this server, tell the user and offer
to save the text with `save_doc` instead.
