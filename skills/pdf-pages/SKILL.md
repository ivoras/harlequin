---
name: pdf-pages
description: Cut, split, merge or reorder the pages of existing PDF documents — extract certain pages into a new PDF, split one PDF into a file per person or chapter, join several PDFs, find which pages names or phrases are on, and e-mail the resulting files. load_skill this whenever the user asks to extract/take out/pull pages, split or merge PDFs, or "find these people in the PDF and make a PDF of them".
---

# Working with pages of existing PDFs

`pdf_pages` copies pages out of PDF documents into new PDF files. The pages are
copied exactly as they are — nothing is re-typed, the layout stays. You only
decide **which pages** go into **which file**.

Do not try to read the PDF and write it again, and do not use `create_pdf` for
this: `create_pdf` makes a new document from text you write; `pdf_pages` copies
existing pages.

## Step 1 — find the page numbers

If the user gave page numbers, skip this step.

If the user gave names or phrases (e.g. a list of employees), look them **all**
up in **one** call:

`check_text({"doc": "u.12", "texts": ["Ana Horvat", "Ivan Kovač", "Marko Babić"]})`

Never call `check_text` once per name. The result is one line per phrase:

- `"Ana Horvat": page 14, 15` — use these pages.
- `"Ivan Kovač": not as an exact phrase, but all its words are on page 31` — the
  name is probably printed differently (e.g. `KOVAČ Ivan`). Usable, but tell the
  user it was matched loosely.
- `"Marko Babić": NOT FOUND` — do not guess a page. Tell the user this one was
  not found. If they can give another identifier (an ID number such as OIB, an
  employee number), look that up instead.

If nothing at all is found, the PDF is probably a scan without text; say so.

## Step 2 — create the files, all in one call

One file with several people's pages:

`pdf_pages({"doc": "u.12", "outputs": [{"title": "Selected payslips", "pages": "14,15,31"}]})`

One file per person — pass **all** outputs in one call (up to 200):

```json
{"doc": "u.12", "outputs": [
  {"title": "Payslip Ana Horvat", "pages": "14,15"},
  {"title": "Payslip Ivan Kovač", "pages": "31"}
]}
```

Merging PDFs, or mixing pages of several PDFs — use `parts`, taken in order:

```json
{"outputs": [{"title": "Contract with annexes", "parts": [
  {"doc": "u.20"},
  {"doc": "u.21", "pages": "2-5"}
]}]}
```

Rules:

- `pages` is a list like `3,7,10-12`. Pages are numbered from 1. The order you
  write is the order in the new file. `all` (or leaving it out) means every page.
- Leaving pages out removes them; writing them in another order reorders them.
- If any output is wrong (a page that does not exist, a missing title) nothing
  is created and the message says what to fix. Fix it and call again.
- Only PDF documents have pages. Word, Excel or text documents are refused.

## Step 3 — report

Tell the user which files were created (ids such as `u.41` and their titles and
page counts), which names were matched only loosely, and which were not found.

## Sending the files by e-mail

Only if the user asked for it, and only to the addresses the user gave. One
call per recipient, with the file in `attachments`:

`send_email({"to": "ana@example.com", "subject": "Payslip October 2026", "markdown": "Dear Ana,\n\nyour payslip is attached.", "attachments": ["u.41"]})`

When different people get different files, check **each** file against **its**
recipient before each call — a payslip sent to the wrong person cannot be taken
back. If you do not have an address for someone, ask the user; never guess one.
