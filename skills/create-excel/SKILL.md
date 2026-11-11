---
name: create-excel
description: Create an Excel spreadsheet (.xlsx) the user can download — from data you gathered, a table you built, or an edited version of an uploaded spreadsheet. load_skill this whenever the user asks for an Excel file, a spreadsheet, an .xlsx, a workbook, or "export this as a table/Excel". It explains the exact Markdown format the create_excel tool converts.
---

# Create an Excel workbook

You write the workbook as Markdown — headings and pipe tables — and the
`create_excel` tool turns it into a real `.xlsx`, saved in the user's files
(the project's, in a project session). It is the same Markdown an uploaded
spreadsheet is read as, so an existing workbook can be edited by reading it,
changing the tables, and creating it again.

## The format

```markdown
# Sales 2026
| Region | Units | Unit price (EUR) | Revenue (EUR) |
|---|---|---|---|
| North | 120 | 9.50 | =B2*C2 |
| South | 80 | 10.00 | =B3*C3 |
| Total | =SUM(B2:B3) | | =SUM(D2:D3) |

| Note | Value |
|---|---|
| Source | Q1 report |

# Assumptions
| Item | Value |
|---|---|
| VAT rate | 0.25 |
```

Rules — follow them exactly:

1. **Sheets.** A heading line (`# Name`) starts a new sheet called `Name`. Use
   short names (max 31 characters, no `[ ] : * ? / \`). Tables before the first
   heading go on a first sheet called "Sheet1". Put nothing but tables under a
   heading: other text (paragraphs, bullet lists) is left out of the workbook.
2. **Tables.** Every row is one line starting and ending with `|`, with the same
   number of cells in every row. The **first row is the header** (it is made
   bold). The `|---|---|` line after it is optional. Every table needs at least one data row under the header; a header-only table is rejected. Separate tables with a blank
   line; tables on the same sheet are placed one under another with an empty row
   between them.
3. **Numbers.** Write a number plainly so it becomes a real numeric cell:
   `1500`, `-2.5`, `0.95`, `1e6`. No thousands separators (`1,500` and `1.500`
   become text), no currency signs or units in the cell (`€5`, `5 kg` are text) —
   put the unit in the column header instead: `Price (EUR)`. Use a decimal point,
   not a comma. Write percentages as fractions (`0.15`) if they will be calculated
   with, or as text (`15%`) if not.
4. **Formulas** start with `=` and use Excel syntax with English function names
   and `,` between arguments: `=SUM(B2:B9)`, `=B2*C2`, `=IF(B2>100,"high","low")`.
   Cell references follow the layout: the header row is row 1, the first data row
   row 2, columns A, B, C… from the left. A second table on the same sheet starts
   one empty row below the end of the first (its header is at last-row + 2), so
   prefer one table per sheet when you use formulas. Excel computes formulas when
   the file is opened.
5. **Text that looks like a number but isn't one** — codes with leading zeros
   (`007`), ids longer than 15 digits, phone numbers — is kept as text
   automatically. Dates are text: write them as `2026-03-31`.
6. **Special characters.** A `|` inside a cell must be written as `\|`. Line
   breaks inside a cell are not supported; keep each cell on one line.
7. **Not supported:** merged cells, colours, fonts, charts, images, comments,
   column formats. Say so if the user asks for them.

## Workflow

1. Shape the data into tables first (from `search_docs`, `read_doc`, web pages,
   calculations). Check every row has as many cells as its header.
2. Small workbook: call
   `create_excel({"title": "Sales 2026", "content": "<the Markdown>"})`.
   Large workbook (more than ~100 rows, or several sheets): build it in the
   sandbox with `edit_file` — `append` one sheet or a few hundred rows per call
   to `tmp://sales.md` — then call
   `create_excel({"title": "Sales 2026", "source": "tmp://sales.md"})`.
3. Tell the user the workbook was created (its id, e.g. `u.42`, and file name)
   and that they can open or download it from their files. Mention anything you
   left out (see rule 7).

**Editing an uploaded spreadsheet.** To change an uploaded workbook and keep
its formatting, formulas and other sheets, use `edit_doc` (load_skill
"edit-document") — it changes the file in place. A workbook you created with
this tool can be changed the same way (`edit_doc` replace, insert or append).
To make a **new** workbook from an old one, `read_doc` the spreadsheet (e.g.
`u.6`) to get its tables in this same format (each sheet's tables follow one
another; sheet names are not included, so add `# Name` headings if you know
them), make the requested changes, and `create_excel` a new file — the original
is left unchanged. Keep values exactly as they were unless asked to change them.

If `create_excel` reports an error, fix the Markdown as the message says and
call it again.
