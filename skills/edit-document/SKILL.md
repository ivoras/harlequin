---
name: edit-document
description: Change an existing document in place — fix or replace words, add a paragraph, a table row or a list item, add text at the end, set or add cells in an Excel workbook, or take the last change back. load_skill this whenever the user asks to change, fix, correct, extend, add to, or remove something from a document that already exists (a note, a report, an uploaded Word or Excel file, or a PDF/Word/Excel file you created earlier). It explains how to find the passage and which edit_doc operation to use.
---

# Edit an existing document

`edit_doc` changes a saved document **where it is**. The document keeps its id,
title and place. You never save a copy unless the user asks for one.

Do the steps in order. Do not skip step 2.

## Step 1 — find the document

- The user gave an id (`u.12`) or attached the file: use it.
- The user named it loosely ("the contract", "the October payroll"): call
  `list_documents` and pick the document whose title fits.
- Two documents fit: ask the user with `ask_user`. Do not guess.

## Step 2 — read the part you will change

- Normal document: `read_doc({"doc": "u.12"})`. A long document: find the place
  first with `search_docs` or `check_text`, then read around it.
- **Excel workbook: `read_doc({"doc": "u.12", "cells": true})`.** It shows every
  sheet with its name, the column letters and the row numbers. You need this to
  know the cell addresses. Never guess an address.

## Step 3 — choose ONE operation and call `edit_doc`

One operation per call. Several changes mean several calls.

| The user wants … | Use … |
|---|---|
| change or fix some words | replace: `old_string` + `new_string` |
| a new paragraph next to some text | insert: `insert` + `after` (or `before`) |
| something added at the end | append: `append` |
| another document's text added at the end | append_source: `append_source: ["u.18"]` |
| a new table row or list item | insert with ONE row `| a | b |` or ONE item `- text` |
| Excel: change cell values | `cells`: `{"October!B2": 1700}` |
| Excel: add rows at the bottom | append: `| Ana | 1500 |` with `sheet` |
| Excel: add rows in the middle | insert: `after` / `before` as `row 5` or a quoted row |

Examples (invented):

- Replace: `edit_doc({"doc": "u.12", "old_string": "30 days", "new_string": "60 days"})`
- Insert a paragraph: `edit_doc({"doc": "u.12", "insert": "Late payment accrues interest.", "after": "Payment is due in 30 days."})`
- Append: `edit_doc({"doc": "u.12", "append": "Signed in two copies."})`
- Add a table row: `edit_doc({"doc": "u.12", "insert": "| Deposit | 900 EUR |", "after": "| Rent | 500 EUR |"})`
- Excel cells: `edit_doc({"doc": "u.7", "cells": {"October!B2": 1700, "October!C2": "=B2*0.73"}})`
- Excel row: `edit_doc({"doc": "u.7", "sheet": "October", "append": "| Marta Babić | 1800 |"})`
- Excel replace: `edit_doc({"doc": "u.7", "old_string": "Travel", "new_string": "Travel costs"})`

In Excel `cells`, a number is a number, text starting with `=` is a formula,
other text is text, and `null` clears the cell. Use the `sheet` argument when
the workbook has several sheets.

**After adding Excel rows, check the formulas.** Two things go wrong easily:

- A formula you put in a new row must use that row's own number. A row that
  lands in row 4 needs `=B4*0.73`, not a copy of `=B3*0.73`.
- A total such as `=SUM(B2:B3)` does **not** grow when a row is added right
  below its last row. Read the sheet again with `cells: true`, and if the total
  leaves the new row out, fix it with `cells`, e.g. `{"B5": "=SUM(B2:B4)"}`.

## What happens, by kind of document

- **Text and Markdown documents:** the text is edited directly.
- **PDF, Word and Excel files you created** (`create_pdf`, `create_word`,
  `create_excel`): the text they were made from is edited and the file is
  rebuilt. Use replace, insert or append. Not `cells`.
- **Word and Excel files the user uploaded:** changed inside the original file.
  Fonts, styles, headers, formulas, other sheets and page setup stay. Use
  replace, insert or append (Word), or `cells`, append, insert, replace (Excel).
  A unified `diff` does not work on them.
- **PDF files the user uploaded:** they cannot be edited. Say so. Offer a new
  document made from the PDF's text (`create_pdf` or `create_word` with
  `source`), or cutting pages with `pdf_pages`.

## Rules

1. Quote a passage **exactly as `read_doc` shows it**. Copy it. Do not retype it
   from memory.
2. If the tool says the passage occurs several times, quote more of the text
   around it so it is unique.
3. A passage must be inside one paragraph. Do not quote across two.
4. Edit only what the user asked for. Do not "tidy" other text.
5. If an error says something is not supported (tracked changes, a text box, a
   macro workbook, a new table, a chart in the way), tell the user plainly what
   the message says. Do not try another way to force it.
6. Do not create a new document to avoid an error, unless the user agrees.

## Step 4 — tell the user

Say what you changed, in which document (id and title), in one or two sentences.
Add that you can take the last change back if they ask (you do it with
`edit_doc` and `undo: true`), and that only one step back is kept.
