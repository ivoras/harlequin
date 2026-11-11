---
name: create-pdf
description: Create a PDF document the user can download — a report, letter, summary, list or table written by you, or an existing text document turned into a PDF. load_skill this whenever the user asks for a PDF, "as a PDF", a printable document, or to export/save something as PDF. It explains the Markdown (and, for special layouts, HTML) the create_pdf tool typesets.
---

# Create a PDF document

You write the document as ordinary Markdown and the `create_pdf` tool typesets
it into a real PDF (A4 pages, page numbers at the bottom), saved in the user's
files (the project's, in a project session). You do not design the page: the
layout, fonts and colours are fixed.

## The format

```markdown
# Payroll report — October 2026

Prepared for the finance team. **Totals are in EUR.**

## Summary

- 42 employees paid
- Total net: 51.230,40

| Employee | Gross | Net |
|---|---:|---:|
| Ana Horvat | 1.620,00 | 1.184,35 |
| Ivan Kovač | 2.469,12 | 1.730,08 |

\newpage

## Notes

1. Figures come from the October payslips.
2. Overtime is included.
```

Rules — follow them exactly:

1. **Start with one `# Title` line.** It is the title printed at the top. The
   tool's `title` argument is only the file name and is not printed.
2. **Use `##` and `###` for sections.** Leave an empty line before and after
   every heading, list, table and paragraph.
3. **Tables** are pipe tables: every row is one line starting and ending with
   `|`, with the same number of cells, and the second line is the `|---|---|`
   separator. Write `---:` in the separator to right-align a column (use it for
   numbers and amounts). A long table continues on the next page by itself and
   repeats its header row.
4. **New page:** put `\newpage` alone on a line, with an empty line before and
   after it. Use it only where the user wants a page to start (e.g. one person
   per page); otherwise let the text flow.
5. **Wide tables** (more than about 6 columns): pass `"landscape": true`.
6. **Text styles:** `**bold**`, `*italic*`, `- ` bullet lists, `1. ` numbered
   lists, `> ` quotes, `---` for a horizontal line, `[text](https://…)` links.
7. **Write amounts and dates as the user's language writes them** (e.g.
   `1.620,00 EUR`, `31.10.2026.`). They are printed as text, exactly as written.
8. **Not supported in Markdown:** images, logos, colours, fonts, font sizes,
   columns. HTML tags inside Markdown are removed. If the user asks for colours,
   columns or a special layout, use the HTML format below; images and logos are
   not available either way — create the PDF without them and say so.

## Where the text comes from

Give the text in exactly one of these ways:

| You have | Call |
|---|---|
| a short text (up to about two pages) | `"content": "<the Markdown>"` |
| a long text | build `tmp://report.md` with `edit_file` (`append`), then `"source": "tmp://report.md"` |
| a saved text document the user wants as a PDF | `"source": "u.12"` (its id) |
| several documents in one PDF | `edit_file({"path": "tmp://all.md", "append_source": ["u.12", "u.13"]})`, then `"source": "tmp://all.md"` |

## HTML format (only for special layouts)

Use Markdown unless the user asks for something it cannot do (colours, two
columns, a letter layout, exact spacing). Then write HTML and pass
`"format": "html"`:

```html
<style>
  h1 { color: #1a5fb4; }
  .cols { columns: 2; }
  .right { text-align: right; }
</style>
<h1>Offer</h1>
<p class="right">Zagreb, 31.10.2026.</p>
<div class="cols"><p>Left column text…</p><p>Right column text…</p></div>
<div class="pagebreak"></div>
<h2>Terms</h2>
```

- Put all CSS in one `<style>` element at the top. Use simple CSS: colours,
  font sizes, margins, `text-align`, `columns`, table borders.
- `<div class="pagebreak"></div>` starts a new page.
- Scripts, forms, and anything loaded from elsewhere (images, fonts,
  stylesheets by URL) are removed. The tool tells you how many items it removed.
- Do not write `<html>`, `<head>` or `<body>`; the page, its size (A4) and the
  page numbers are added for you.

## Workflow

1. Gather and check the content first (`search_docs`, `read_doc`, `calculator`
   for every amount). The PDF is only as correct as the Markdown.
2. Short document (up to about two pages): call
   `create_pdf({"title": "Payroll report October 2026", "content": "<the Markdown>"})`.
   Longer document: build it in the sandbox with `edit_file` — `append` one
   section per call to `tmp://report.md` — then call
   `create_pdf({"title": "Payroll report October 2026", "source": "tmp://report.md"})`.
3. Tell the user the PDF was created (its id, e.g. `u.42`, the file name and the
   number of pages) and that they can open or download it from their files.
   Repeat any note the tool returned (e.g. images left out).

To change a PDF you created, change the Markdown and call `create_pdf` again: it
makes a new file.

`create_pdf` writes **new** documents only. It cannot cut pages out of an
existing PDF, merge PDF files page by page or fill in a PDF form — say so if the
user asks. (Turning the *text* of a PDF into a new PDF works: use its id as
`source`; the original layout is not kept.)

If `create_pdf` reports an error, fix what the message says and call it again.
If it says PDF creation is not set up on this server, tell the user and offer to
save the text as a document with `save_doc` instead.
