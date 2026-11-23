# Built-in tools

These are the tools the Harlequin agent exposes to the LLM during a turn. The
model calls them by name with JSON arguments; results are fed back into the loop.
Some tools are only present when the corresponding feature is enabled in
`server.yaml` (noted per tool). Registered in `buildTools`
(`internal/server/agent/tools.go`; larger tools live in sibling files there).
The Documents section is grouped by what the user is trying to do.

Beyond these built-ins, two families of tools appear dynamically:
- **Skill tools** — namespaced `<skill>.<tool>`, declared by a skill's frontmatter
  and backed by sandboxed JS. Visible per the active hat/skill set. Their `run` JavaScript has the same helpers as `run_js` (`read_doc`, `sheets.*`, `xml.*`, `tmp`/`storage`, `load`/`include`), with the call's arguments as `args`.
- **MCP tools** — namespaced `mcp__<server>__<tool>`, proxied from external MCP
  servers (only when `mcp.enabled`). Enabled servers whose OAuth sign-in is
  missing are skipped, and the system prompt tells the model which ones so it can
  ask the user to sign in.

---

## Memory

### `memory_search`
**Why:** recall durable facts/preferences about the user and their org before
answering, so the agent stays consistent across sessions.
Searches personal + shared memory, plus the project's in a project session;
`all_projects: true` also covers every project the user belongs to (hits there
are labelled `p<project>.N` and are read-only). See [docs/SCOPES.md](docs/SCOPES.md).
**Example:** `memory_search({"query": "preferred currency"})`
→ ranked hits, each with a composite id (`u.N`/`s.N`/`p.N`) and `slot_key` to use
with `memory_change`/`memory_delete`.

### `memory_feedback`
**Why:** after a `memory_search` whose results informed the answer, cite the
memory ids that were actually useful. Measures memory usefulness (no learning
yet); the model is told to call it whenever recalled memories helped.
**Example:** `memory_feedback({"ids": ["u.4", "s.7"], "reason": "currency and company name"})`

### `memory_write`
**Why:** persist a new durable fact. `scope` is `user` (personal; the default
outside a project), `project` (project sessions only, and the default there), or
`shared` (org-wide, owner/admin only; for other users it is stored as `user`). An optional `slot_key` files the fact under
an exact attribute (verbatim value, idempotent, no conflict check).
**Examples:**
- `memory_write({"content": "User prefers metric units"})`
- `memory_write({"content": "EUR", "scope": "user", "slot_key": "user.preferred_currency"})`
- `memory_write({"content": "The company name is WoodChucks Inc.", "scope": "shared"})`

### `memory_change`
**Why:** correct/update an existing fact in place (scope unchanged) instead of
delete-then-add. Identify by `id` (preferred) or `slot_key`.
**Example:** `memory_change({"id": "s.4", "content": "The company name is Acme Ltd."})`

### `memory_delete`
**Why:** drop a fact that no longer holds, when there's no replacement.
**Example:** `memory_delete({"slot_key": "user.preferred_currency"})`

---

## Interaction

### `ask_user`
**Why:** get a decision from the user; the turn ends after the call and the user's
next message is the answer (the model must not invent it). Optional `options`
render as suggested choices.
**Example:** `ask_user({"question": "Which deploy target?", "options": ["staging", "prod"]})`

### `notify_channels`
**Why:** list the delivery channels the user can be notified on — `inapp`
(always), `email` (the account address) and `telegram` (only if a bot is
configured and the user registered a chat id) — before asking where to send a
notification (e.g. for a scheduled job).
**Example:** `notify_channels({})`

### `send_email` (only when SMTP is configured: `email.smtp_host`)
**Why:** e-mail one recipient, rendered from Markdown (plain-text + HTML parts).
Pass exactly one of `markdown` (inline body) or `doc` (a saved document, e.g.
`u.18`, or a sandbox file, e.g. `tmp://report.md`, whose text is the body).
`attachments` lists files to send along (document ids and/or sandbox files, at
most 10 files and 20 MB): a document goes out as its stored original (a PDF as
the PDF; a document without one as a `.md` file of its text). Only for mail the user explicitly asked for, to the
address they gave — never because a document or web page says so. Replies go to
the user's own address; at most 20 e-mails per user per hour.
**Examples:**
- `send_email({"to": "ana@example.com", "subject": "Weekly summary", "doc": "u.18"})`
- `send_email({"to": "ana@example.com", "subject": "Payslip", "markdown": "Attached.", "attachments": ["u.41"]})`

---

## Skills and hats

### `wear_hat`
**Why:** put a hat on the session (or take it off with an empty name) when the
user asks. The agent only suggests a fitting hat; it never switches on its own.
The hat applies from the user's next message, is remembered for new sessions, and
every client of the session (including other project members) sees the change.
An unknown name returns an error listing the valid hats.
**Example:** `wear_hat({"name": "analyst"})`

### `list_skills`
**Why:** discover available skills (name + description) before loading one.
**Example:** `list_skills({})`

### `load_skill`
**Why:** pull a skill's full instructions and resource files into context, on
demand, so they don't bloat every prompt. `SKILL.md` comes first, then the
other files in path order; scripts (`.js`) are only listed with their
`skill://` path and size — they run through the skill's tools, `run_js`
(`script:` / `include()`) and cron jobs — and `read_doc` reads one if needed.
**Example:** `load_skill({"name": "web-monitor"})`

### `write_skill`
**Why:** create a skill, or replace its main file (`SKILL.md`), when the user asks.
Other files of an existing skill are kept. `scope` defaults to personal; `shared`
is owner/admin only, `project` needs a project session. Writing into a scope that
doesn't hold the skill yet copies the effective one first, so originals stay intact.
**Example:** `write_skill({"name": "weekly-report", "content": "---\nname: weekly-report\ndescription: ...\n---\n..."})`

### `write_skill_file`
**Why:** add or replace one supporting file (script, template) of an existing
skill; it is then reachable as `skill://<name>/<path>`. Same scope rules.
**Example:** `write_skill_file({"name": "weekly-report", "path": "scripts/run.js", "content": "..."})`

### `delete_skill`
**Why:** delete a skill from one scope, only on explicit request. Defaults to the
personal scope (never the project's); deleting a personal copy reveals the shared
skill again.
**Example:** `delete_skill({"name": "weekly-report"})`

---

## Compute & code

### `calculator`
**Why:** do exact arithmetic instead of guessing; LLMs are unreliable at math.
Exact decimal arithmetic (no floating-point error: `0.1 + 0.2` is `0.3`): `+ - * / % ^`,
parentheses, `round(x, digits)` (halves away from zero; a top-level `round(x, 2)` prints
two decimals), `floor`, `ceil`, `trunc`, `abs`, `min`, `max`. A non-terminating result such
as `1 / 3` is shown to 20 decimals with its exact fraction. Syntax outside that grammar
(`Math.sqrt`, fractional powers, `.toFixed`) falls back to floating-point JavaScript and the
result says so (`^` stays power there). Thousands separators (`1,234.56`) are an error. The system prompt tells the model to use it for all money figures.
**Example:** `calculator({"expression": "round(1500 * 1.08, 2)"})` → `1620.00`

### `run_js`
**Why:** general escape hatch — run JavaScript in a sandbox for logic, parsing,
HTTP, and per-user file storage that the model can't do itself. The engine (goja)
is **ES5.1-compatible and supports much of ES6** (let/const, arrow functions,
template literals, classes, destructuring, spread/rest, `for…of`, generators,
Map/Set, Symbol, Promise, typed arrays, BigInt); emit output with `println()`/`print()`.
Helpers: `fetch(url)`, `dom.parse/query/grep/json`, per-user `tmp.*`/`storage.*`
stores, and `load(uri)`/`include(uri)` for `skill://`/`storage://`/`tmp://` scripts.
For documents: `read_doc(ref)` returns a document's (or sandbox file's) full text;
`sheets.query(doc, sql[, {sheet, header_row, more_docs}])` runs a `query_sheet`
SELECT and returns row objects (columns in SELECT order, at most 200,000 rows);
`sheets.tables(doc)` lists the tables and columns; `xml.query(doc, path)` /
`xml.outline(doc)` read XML like `query_xml`. Scripts should read data this
way, never as literals typed into the code.
**Examples:**
- inline: `run_js({"code": "var r = fetch('https://example.com'); println(r.status);"})`
- saved script + args: `run_js({"script": "skill://web-monitor/lib/check.js", "args": {"name": "fzoeu"}})`

---

## Sandbox files

### `edit_file`
**Why:** edit a sandbox file (`tmp://`, `storage://`; a bare path means `tmp://`)
in place without re-sending it. One operation per call, all-or-nothing (errors say
what to fix):

| Operation | Arguments | Use |
|---|---|---|
| Replace | `old_string` + `new_string` (`replace_all`) | fix or delete a passage |
| Patch | `diff` (unified diff; hunks located by content) | several changes at once |
| Append | `append` | add text the model writes; creates the file |
| Append from | `append_source` (list of documents and/or sandbox files) | concatenate whole texts server-side, any length; creates the file |
| Insert | `insert` + one of `after` / `before` (a passage quoted exactly, must occur once) | add a paragraph, table row or list item next to a passage |

Insert puts the text in its own paragraph beside the block (run of non-empty
lines) holding the passage, with one empty line between; a single table row or
list item quoted next to a row or item of the same kind goes on the adjacent
line. The rest of the text is untouched.

Publish the result with `save_doc(source=...)`. Given a document id as `path` it
behaves as `edit_doc`.
**Examples:**
- `edit_file({"path": "tmp://combined.md", "append": "## Part 2\n…"})`
- `edit_file({"path": "tmp://combined.md", "append_source": ["u.18", "u.19", "tmp://annex.md"]})`
- `edit_file({"path": "tmp://combined.md", "old_string": "Članak 5.", "new_string": "Članak 5.a"})`

### `Grep`
**Why:** ripgrep-style content search over files saved in your sandbox namespaces
(`tmp://`, `storage://`) — e.g. a page saved by `WebFetchDOM`'s `save_file` or a
stored document — without reading the whole file. `output_mode` is
`files_with_matches` (default), `content`, or `count`; supports `glob`, `type`,
`-A`/`-B`/`-C`, `-i`, `-n`, `multiline`, `head_limit`.
**Examples:**
- `Grep({"pattern": "Ryzen", "path": "tmp://news.html", "output_mode": "content", "-n": true})`
- `Grep({"pattern": "error", "path": "tmp://", "glob": "*.json", "output_mode": "count"})`

---

## Web

### `WebSearch` (only when a Brave Search API key is set: `web_search`, env `BRAVE_API_KEY`)
**Why:** search the web for current information or for pages to read with
`WebFetch`. Returns title, URL, snippet and age per hit, most relevant first;
`allowed_domains` / `blocked_domains` filter by site.
**Example:** `WebSearch({"query": "EU AI Act implementation timeline", "allowed_domains": ["europa.eu"]})`

The tools below are on by default and disabled with `agent.web_fetch.enabled: false`.
They share one fetch pipeline (browser-like headers and TLS fingerprint, SSRF
guard, 15-minute cache, and the Zyte fallback for 4xx when `ZYTE_API_KEY` is set).

### `WebFetch`
**Why:** read a web page and get an AI answer about it — fetches the URL, converts
HTML→Markdown, and analyses it with a small model. Best for "what does this page
say" questions. (15-min cache; reports cross-host redirects to call again.)
**Example:** `WebFetch({"url": "https://example.com/pricing", "prompt": "What are the plan tiers and prices?"})`

### `MarkdownFetch`
**Why:** get a URL's *content* as Markdown, not an AI answer — downloads the page or
document (PDF, DOCX, PPTX, XLSX, HTML, Markdown, CSV) and converts it with Docling
(layout analysis, tables). Use when you need the full text or faithful structure
(to quote it, or `save_doc` it) or when the user wants the entire unabridged page;
use `WebFetch` for a quick answer from an ordinary page. Web pages are fetched via
the Zyte browser API when `ZYTE_API_KEY` is set (documents are downloaded directly). Only registered when a document converter (`documents.docling`/`xberg`) is
configured. Output is capped at 1,000,000 characters (marked when truncated). The
result is in the model's context afterwards; the description tells it to use it
directly rather than fetching the same URL again.
**Example:** `MarkdownFetch({"url": "https://example.com/annual-report.pdf"})`

### `WebFetchGrep`
**Why:** deterministic, exhaustive check for a value on a page — a
case-insensitive RE2 regex over the fetched raw HTML, returning the matching
lines. With an optional `prompt`, the matches go to the small analysis model and
its answer is returned instead (like `WebFetch`, but over only the matched lines).
**Examples:**
- `WebFetchGrep({"url": "https://shop.example/item/42", "pattern": "price[^<]{0,40}"})`
- `WebFetchGrep({"url": "https://shop.example/item/42", "pattern": "in stock|sold out", "prompt": "Is it available?"})`

### `WebFetchDOM`
**Why:** precise scraping, not summarisation — returns the page's HTML structure as
JSON (candidate lists with ready-to-use CSS selectors; `selector=` for one record
per list item; `grep=` for a value with its surrounding context). Use to read,
compare, filter, or count list items, or to set up a durable extractor/monitor.
Pass `save_file` to keep the raw page under `tmp://` (the result returns the full
path) for follow-up searching with `Grep`.
**Examples:**
- discover: `WebFetchDOM({"url": "https://news.site/list"})`
- list items: `WebFetchDOM({"url": "https://news.site/list", "selector": "li.headline"})`
- locate a value: `WebFetchDOM({"url": "https://news.site/list", "grep": "Breaking:"})`
- save for grepping: `WebFetchDOM({"url": "https://news.site/list", "save_file": "news.html"})`

---

## Documents

Searches and listings cover personal + shared documents, plus the project's in a
project session; ids are `u.N` / `s.N` / `p.N`, and `p<project>.N` (e.g. `p3.17`)
for another project's document. See [SCOPES.md](SCOPES.md).

**Documents and sandbox files.** Besides saved documents the agent has sandbox
files (`tmp://…` scratch space, `storage://…` kept per user; a bare path such as
`notes.md` means `tmp://notes.md`). Wherever it makes sense a tool takes either:

| Tool | Document id | Sandbox file |
|---|---|---|
| `read_doc`, `check_text` | yes | yes (`doc`) |
| `edit_doc` / `edit_file` | yes | yes (each accepts the other's target) |
| `append_source` of the edit tools | yes | yes |
| `create_excel`, `create_pdf`, `create_word` (`source`) | yes | yes |
| `save_doc` (`source`) | no — use `copy_doc` to duplicate a document | yes |
| `copy_doc` (`source`; `target` is a sandbox file) | yes | yes |
| `send_email` (`doc`, `attachments`) | yes | yes |
| `search_docs`, `align_docs`, `list_documents` | yes | no — they work on the search index; `save_doc` a file first, or use `Grep` on it |
| `pdf_pages` | yes (PDF documents) | no |
| `edit_pdf` (`doc`; `save_to` is a sandbox file) | yes (PDF documents) | yes (`.pdf` files) |
| `query_sheet` (`doc`, `more_docs`; `save_to` is a sandbox file) | yes (spreadsheets; PDF/Word tables) | yes (`.xlsx`, `.csv`, `.md` …) |
| `query_xml` | yes | yes (`.xml`) |
| `move_doc`, `delete_doc` | yes | no |

### Find and read

Locate a document or a passage, and read it.

#### `list_documents`
**Why:** enumerate the corpus (scoped id, title, LLM-generated catalogue
description, size, date) so the model can resolve a loosely-named document
("the new EEA regulation") to its id for `align_docs` or a `search_docs`
docs filter. `all_projects: true` also lists every project the user belongs to.
**Example:** `list_documents({})`

#### `search_docs`
**Why:** retrieve passages from the organisation's RAG document corpus (uploaded
PDFs/text) to ground answers in source material. The optional `docs` filter
restricts hits to specific documents — use it whenever the question is about a
particular document (or pair of documents). `all_projects: true` also searches
every project the user belongs to.
**Example:** `search_docs({"query": "OTC derivatives reporting obligations", "docs": ["p.3"]})`

#### `read_doc`
**Why:** read a document's text (its Markdown rendering) by reference — mainly
for files the user attached to their message, listed at its end as
`@"u.18/report.pdf"` — or a text file in the sandbox (`tmp://combined.md`), or a
file of a skill by its `skill://<skill>/<path>` (resolved like `load_skill`). Returns up to `limit` characters (default 30,000) from
`offset`, and says where to continue when truncated. For an image document it
notes whether the model can see the picture (vision) or only its OCR text. To find
something in a long document, `search_docs` with `docs` or `check_text` is better.
**Example:** `read_doc({"doc": "u.18", "offset": 30000})`
`cells: true` on an Excel file renders it from the file itself: one section per sheet (`## Sheet "October" (A1:C4)`), a table whose header row is the column letters and whose first column is the row number, formulas as `=…` text and a `Merged:` line; the usual `offset`/`limit` window applies. On any other document it says the grid view exists only for Excel files.

On a spreadsheet longer than 30,000 characters `read_doc` prepends a notice
pointing to `query_sheet` and, unless `limit` is given, shows only the first
4,000 characters.

#### `query_sheet`
**Why:** answer questions about a spreadsheet (Excel `.xlsx`/`.xls`/`.xlsb`,
OpenDocument `.ods`, CSV, TSV) of any size — or about the tables in a PDF or
Word document — with SQL, without the model reading its rows. The stored original
is streamed into a private, read-only, in-memory SQLite database: one table per
sheet, column names derived from the headers (`"Iznos (EUR)"` → `iznos_eur`), a
`_row` column with the spreadsheet row number. Without `sql` it returns the
schema (column types, original headers, example values, rows above the header)
plus example queries built from the real names; with `sql` it runs one SELECT
over all rows and shows up to 200 result rows. `more_docs` loads more
spreadsheets beside the first for joins and comparisons; `sheet` loads one sheet;
`header_row` overrides header detection (-1 = no header); `save_to`
(`tmp://x.md` / `.csv` / `.json`) writes all result rows (≤ 200,000) to a
sandbox file instead — `.md` feeds `create_excel`'s `source`.
- **Several files:** `more_docs` lists every other file of the question. Each
  table is then named after its file (`"F2 Podaci 03-2026.xlsx"` →
  `f2_podaci_03_2026`, plus the sheet when the file has several), and tables
  with the same columns — across files or sheets — are combined in a view
  `<common prefix>_all` with `_file` and `_sheet` columns; a column that is
  numeric in one table and text in another is text in the view. The schema
  lists views first and abbreviates their member tables to "same columns as …".
- **Matching and reconciling:** `key(x)` normalises identifiers written
  differently by two systems (case, spaces, `- / . _ \` separators, leading
  zeros per digit group: `'1/11/01'` = `'1-11-1'`, `'01270000'` = `1270000`);
  the tool description carries recipes for rows only in A or B, amount
  differences and lookups (VLOOKUP-style joins). The schema shows each
  column's Excel letter, so "column V" can be resolved.
- **Cell colours:** a column whose cells have background fills gets a companion
  `<column>_color` column (type `color`) holding the colour in words and hex
  (`light blue #BDD7EE`; RGB, theme colours with tint and the indexed palette
  are resolved, white counts as none), for sheets that mark rows by colour.
- **PDF / Word / Markdown tables:** a document that is not a spreadsheet is read
  from its converted text: each pipe table becomes a table named after the
  heading above it, tables split across pages (same header) are joined, values
  are typed like CSV values. A sandbox `.md` file with tables works the same.
- **Formats:** the format is detected from the content (zip parts, the OLE
  signature of `.xls`), the name only for CSV/TSV. `.xls`, `.xlsb` and `.ods`
  are converted to `.xlsx` with LibreOffice once per load (the cache keys on
  the original bytes); every format then reads the same: same types, values and
  dates for the same data.
- **How types are decided** — from the values, never from column names. In an
  Excel/ODS file each cell says whether it holds a number or text, and its
  number format whether a number is a date or a zero-padded code. A CSV has no
  types, so its values are read the way a spreadsheet program shows them: plain
  numbers are numbers, `2026-01-21` and `21.01.2026.` are dates, digits with a
  leading zero (`01270000`) are codes, and a column consisting only of amounts
  with separators (`1.234,56`) becomes numbers, its decimal separator decided
  from all its values (`;`-delimited files default to a decimal comma). The
  delimiter is sniffed from the header (`.tsv` is always tab); non-UTF-8 text
  is read as Windows-1250. A column of plain numbers is `integer`/`number` even
  when it holds account numbers: nothing in the values tells them apart, so the
  model sees the header and samples, and comparisons with a `code` column still
  match.
- **Types:** `integer`, `number`, `date` (ISO text `YYYY-MM-DD`, from the cell's
  date format), `code` (pure-digit identifiers such as account numbers, kept as
  text with their leading zeros; numbers mixed into such a column become text;
  a number shown with a zero-padding format such as `00000000` is read as the
  padded text), `text-number` (amounts written as text — wrap in `num()`, which
  reads `1.234,56`, `1,234.56`, `12,5 €`, `(123)`), `text`, `mixed`, `empty`.
  Declared column types make an `integer` account column join a `code` one
  (`1270000` = `'01270000'`); the schema flags code columns that held bare
  numbers, whose leading zeros may be missing (`key(col)` matches them).
- **Safety:** an authorizer allows only reads (no writes, `ATTACH`, `PRAGMA`,
  extensions); queries stop after 30 s. One load is capped at 20M cells.
- **Caching:** each document's tables are kept on disk next to its stored
  original as `<stored>.sheetdb.<hash>.v<n>` (built right after upload for
  spreadsheets, otherwise on first use), so a returning user's query opens the
  file instead of reloading the workbook (3M cells: 0.2 s instead of ~11 s).
  The name carries the content hash and the loader version, so an edited file
  or a changed loader never reads an old cache; files are rebuilt after a week
  and swept hourly when older, deleted with their document, and not copied
  with it. Several documents are assembled from their cache files in memory
  (12 files of 100k cells: ~0.2 s). Sandbox files and non-default
  `sheet`/`header_row` loads are not cached on disk. Open databases are kept
  in memory (6 at most, 15 idle minutes, a week at most).
**Example:** `query_sheet({"doc": "u.12"})`, then
`query_sheet({"doc": "u.12", "sql": "SELECT kupac, SUM(iznos) AS ukupno FROM prodaja GROUP BY kupac ORDER BY ukupno DESC LIMIT 10"})`

#### `query_xml`
**Why:** read an XML document — UBL e-invoices (e-Račun), bank statements,
exports — without the model reading raw XML. Without `path` it returns an
outline: every element path once, indented, with its count (`×N`), attributes
and first value. With `path`, or `paths` for many fields in one call, it returns
the values with their exact positions (`/Invoice/cac:InvoiceLine[2]/cbc:ID`).
Paths are a small XPath: `/a/b` from the root, `//b` (or a bare `b`) anywhere,
`*`, `..`, `[2]`, `[@attr]`, `[@attr='v']`, `[child]`, `[child='v']`, and a final
`/@attr`; names match with or without their prefix. A path that matches nothing
says `not found`, which is how required elements are checked. The stored
original's exact bytes are read (declared encodings such as windows-1250 are
honoured); malformed XML is reported. In `run_js`, `xml.query(doc, path)` and
`xml.outline(doc)` do the same for scripts over many files. XML uploads are
typed `text/xml`; `read_doc` on one points to `query_xml`.
**Example:** `query_xml({"doc": "u.7", "paths": ["/Invoice/cbc:ID", "//cbc:PayableAmount", "//cac:AccountingSupplierParty//cbc:EndpointID"]})`

#### `check_text`
**Why:** authoritatively confirm or rule out that an exact phrase exists
anywhere in one document — a literal, exhaustive scan, unlike `search_docs`'s
ranked semantic/lexical search. `align_docs` classifying a section as
"only in A/B" is a hint, not proof (renamed or reworded headings can hide the
true counterpart); this is the deterministic check before asserting something
is genuinely new, removed, or absent. `doc` may also be a sandbox text file;
matches are then reported by line number.

With `texts` (a list, up to 500 phrases) it looks them all up in one call and
returns a table of each phrase and where it occurs — the way to find which pages
of a PDF a list of names is on, in one step instead of one call per name. For a
PDF with a text layer the pages are read from the file itself and are exact;
otherwise they come from the index (where a passage starts). Matching ignores
case and line breaks. A phrase not found as such but whose words all occur on
one page ("Ivan Kovač" vs "KOVAČ Ivan") is reported separately as a loose match,
and anything else as NOT FOUND.
**Examples:**
- `check_text({"doc": "p.6", "text": "Fund for Civil Society"})`
- `check_text({"doc": "u.12", "texts": ["Ana Horvat", "Ivan Kovač"]})`

### Compare

Set two documents side by side.

#### `align_docs`
**Why:** compare two corpus documents without holding both in context. The
server aligns their sections deterministically — mode `versions` diffs two
revisions of the same text (identical sections are skipped), mode `topical`
pairs sections of two different texts about the same subject by embedding
similarity (leftovers are reported as present in only one document) — and
returns the pairs in cursor batches for the model to analyse one at a time.
**Example:** `align_docs({"doc_a": "p.3", "doc_b": "p.4", "mode": "topical"})`,
then again with `"cursor": 5` etc. until the last batch. For a question that
names a theme spanning many sections (e.g. "what changed about funding")
rather than one article, use `view="pairs"` with `pairs="9,12,16,19-22"`
(explicit numbers/ranges picked from the summary) or a comma-separated
multi-term `filter` — both fetch several relevant pairs per call instead of
one call per pair.

### Write and change text

Save a text as a document, and edit or extend it afterwards.

#### `save_doc`
**Why:** persist a produced report/analysis into the corpus (personal, or
project in a project session) so later sessions can answer questions about it
via `search_docs`. Citations in the content stay linked. The compare skills use
this to make their reports queryable afterwards. Every long quoted excerpt in
the content must be immediately followed by its `[d.x.N]` citation — `save_doc`
verifies each one against the chunk's real text and **refuses to save** (with
the specific mismatch) if a quote is uncited or cited to the wrong chunk; fix
and call it again. For long texts pass `source` (a sandbox file, e.g. `"tmp://combined.md"`)
instead of `content`: the sandbox file is read server-side, so the text never has
to pass through the model's output.
**Examples:**
- `save_doc({"title": "Comparison: Regulation 2025 vs 2026", "content": "<report>"})`
- `save_doc({"title": "Combined law", "source": "tmp://combined.md", "is_raw": true})`

#### `edit_doc`
**Why:** change a saved document in place — keeps its id, title and scope,
re-indexes it for `search_docs`, and updates the stored original. The same five
operations as `edit_file` (replace, patch, append, append from, insert after/before
a passage) plus `cells` for Excel, one per call, all-or-nothing. How it edits depends on where the
document came from:
- **text/Markdown** (saved with `save_doc`, or uploaded `.md`/`.txt`): the stored
  text is edited.
- **made by `create_excel` / `create_pdf` / `create_word`**: the Markdown (or
  HTML) it was generated from — kept beside the file as `<stored file>.src.json`
  (kind, format, landscape, source text) — is edited and the file is rebuilt with
  the same converter and options. If the new source cannot be converted, or the
  renderer is missing, nothing changes. The result says the file was rebuilt (and
  the PDF's page count).
- **uploaded PDF**: refused (a PDF's pages are fixed); the message points to
  `create_pdf`/`create_word` with `source`, or `pdf_pages`.
- **uploaded Word (`.docx`)**: edited inside the original file
  (`internal/server/docxedit`): only the bytes of `word/document.xml` the edit
  needs are spliced and every other zip entry is copied unchanged, so fonts,
  styles, headers/footers, numbering, images and page setup stay. Passages are
  quoted as `read_doc` shows them (whitespace collapsed; `#`, list markers, `**`,
  `_` and table pipes ignored) and must lie in one paragraph. Replace puts the new
  text in the first run the match touches (its formatting applies); an emptied
  paragraph is removed. Insert (`after`/`before`) and append add paragraphs that
  copy the neighbour's paragraph and run properties (next to a heading: the
  nearest body paragraph's); `#`/`##`/`###` lines use the document's own heading
  styles (found by name, `heading 1` etc.), `**bold**`/`*italic*` and list items
  work, `| a | b |` next to a table row adds a row with the same cell count, plain
  text next to a cell goes into that cell; new tables are not supported. No
  `diff`. Refused: tracked changes in the document, a passage inside a text box,
  a hyperlink or a field (insert a paragraph next to it instead). The result is
  re-parsed and every untouched paragraph checked before the file is replaced. The
  searchable text is derived like an upload's (the configured converter, else
  `docxextract`). Undo works. Not handled: headers/footers/footnotes text,
  merged-cell geometry, content controls' placeholders, text split by soft
  hyphens or symbols, legacy `.doc`.
- **uploaded Excel (`.xlsx`)**: edited inside the original file with excelize
  (`internal/server/xlsxedit`), so styles, number formats, column widths, formulas
  and the other sheets stay. Call `read_doc` with `cells: true` first to get the
  sheet names and cell addresses. Operations: `cells` (an object from address to
  value, `{"October!B2": 1700, "C2": "=B2*0.73"}`; a number is numeric, `=…` a
  formula, other text text, `null` clears; the cell keeps its style; only the
  top-left cell of a merged range is writable), `append` (Markdown table rows
  after the last used row, styled like the row above), `insert` + `after`/`before`
  (table rows next to `row N` or a quoted row; rows below move and formulas,
  merged ranges, names and validation follow), and `replace` (text and formula
  text only; use `cells` for numbers). `sheet` names the sheet for addresses
  without one and for the other operations (optional when there is one sheet);
  `cells` counts as the one operation of a call; `diff` is refused. Formulas are
  recalculated by Excel on open (the workbook is flagged `fullCalcOnLoad`, and a
  written formula's stale cached value is dropped). Before returning the file the
  output is re-read and every cell compared with the input (only the intended
  change and an insert's row shift may differ), and the package's charts, images,
  pivot tables, comments, tables and so on must still be present. Refused: legacy
  `.xls`, encrypted or macro-enabled (`.xlsm`) files, a missing sheet, a bad
  address, a write into a merged range, and row insertion in a workbook with
  charts or pivot tables (their ranges would not move). The searchable text is
  derived like an upload's (the configured converter, else `mdconv`).

Unchanged passages keep their chunk ids across an edit, so citations to them stay
valid. **Undo:** before replacing the stored file the previous version is kept
(`<stored file>.prev`, and `.src.json.prev` for a generated file); `edit_doc({"doc":
"u.12", "undo": true})` restores it, once — one level, overwritten by each edit,
not available for sandbox files, not combinable with an operation. Deleting the
document removes the file, the sidecar and the `.prev` copies; `copy_doc` copies
the sidecar so a copy stays editable. Shared documents: owner/admin only. **Out of place:** `save_as` (a title, optional `scope`) leaves the document unchanged and saves the edited result as a new document — the document is copied (file and source sidecar) and the edit runs on the copy, which is removed again if the edit fails, so a failed call creates nothing; `undo` cannot be combined with it. `undo` also restores a PDF marked in place by `edit_pdf`. Given a
sandbox file as `doc` it behaves as `edit_file`.
**Examples:**
- `edit_doc({"doc": "p.7", "append": "## Addendum\nNew section…"})`
- `edit_doc({"doc": "u.12", "append_source": ["u.18", "tmp://annex.md"]})`
- `edit_doc({"doc": "u.12", "insert": "Late payment accrues interest.", "after": "Payment is due in 30 days."})`
- `edit_doc({"doc": "u.12", "undo": true})`

### Produce files

Make a downloadable file from Markdown (or HTML).

#### `create_excel`
**Why:** produce a downloadable Excel workbook (`.xlsx`) — the reverse of how an
uploaded spreadsheet is read. The input is Markdown: `# Sheet name` headings
start sheets, each pipe table's first row is a bold header, tables on a sheet
are stacked with an empty row between them, plain numbers become numeric cells
(keeping their decimals on display), `=…` becomes a formula (computed when
Excel opens the file), anything else is text. Docling's Markdown for an uploaded
`.xlsx` converts back cell for cell, so a spreadsheet can be edited via `read_doc`
→ change → `create_excel`. Pass `content` inline or `source` (a sandbox file, or a saved document such as
`u.12`, whose text is used). A table must have data rows under its header: a header-only table (typically a call cut off mid-table) is rejected instead of saved as an empty workbook.
Saved like `save_doc` (project in a project session, else personal; shared for
owners/admins), with the Markdown as its searchable text. The `create-excel`
skill teaches the model the format. Converter: `internal/server/mdxlsx`. The
Markdown is kept as the workbook's source, so the result can be changed later with `edit_doc`.
**Example:** `create_excel({"title": "Sales 2026", "source": "tmp://sales.md"})`

#### `create_pdf`
**Why:** produce a downloadable PDF written from scratch — a report, letter,
summary or table "as a PDF". The text comes as `content` (inline) or `source` (a
sandbox file, or a saved document such as `u.12`), in one of two formats:
- **Markdown** (default): headings, lists, pipe tables with `---:` alignment,
  quotes, code, typeset with a fixed house stylesheet. A line containing only
  `\newpage` starts a new page. Raw HTML is dropped.
- **HTML** (`format: "html"`, or recognised from a `.html` source or a leading
  `<!DOCTYPE`/`<html`): for layouts Markdown cannot express. The document's own
  `<style>` rules are kept (after the house stylesheet); `<div class="pagebreak">`
  starts a new page.

Pages are A4 with page numbers; `landscape: true` turns them for wide tables. `save_to` (a sandbox path) writes a temporary file instead of a document (downloadable, see "Downloading sandbox files"), e.g. for chaining into `edit_pdf`.
Images are printed only when inline (`data:`); others are replaced by their alt
text and reported in the result. Saved like `save_doc` (project in a project
session, else personal; shared for owners/admins), with the Markdown (HTML is
converted) as its searchable text. It does not copy or extract pages of existing
PDFs (that is `pdf_pages`).

Security (`internal/server/mdpdf`), three independent layers: the page is rebuilt
from the parsed tree without scripts, frames, forms, embeds, event handlers or
external references (removals are reported); a Content-Security-Policy written
by the server blocks scripts and every non-inline load; and headless Chrome
(`chrome-headless-shell`) runs inside a bubblewrap sandbox with no network, no
host files, an empty environment and file-size/CPU/memory limits. When Chrome or
`bwrap` is not installed the tool stays offered and answers that PDF creation is
not set up. The `create-pdf` skill teaches the model the formats. The source is
kept, so the result can be changed later with `edit_doc` (the PDF is rebuilt).
**Examples:**
- `create_pdf({"title": "Q1 report", "source": "tmp://report.md"})`
- `create_pdf({"title": "Offer letter", "source": "tmp://letter.html", "landscape": false})`

#### `create_html`
**Why:** produce a downloadable, self-contained HTML page — a report, summary or
page of notes "as HTML". Same text-to-file pipeline as `create_pdf`/`create_word`
(`createFile` in `agent/createfile.go`): the text is Markdown (default) or HTML
(`format: "html"`), from `content` or `source`; `mdpdf.Page` sanitises it (scripts,
forms, external images/fonts/stylesheets removed, a Content-Security-Policy added)
and wraps it in the house stylesheet, and that page is the file — no renderer is
needed. Saved as a `text/html` document with a source sidecar, so `edit_doc` can
change it (the page is rebuilt) and `undo` works. No `landscape`. The `create-html`
skill teaches the model.
**Example:** `create_html({"title": "Q1 report", "content": "# Q1\n\nRevenue grew **12%**."})`

**Shared parameters.** All `create_*` tools (`create_pdf`, `create_word`,
`create_excel`, `create_html`) take the same parameters with the same meaning:
`title` (the extension is added), `content` or `source` (a sandbox file or saved
document), `scope`, and `save_to` + `overwrite`. `save_to` (a sandbox path such as
`tmp://report.pdf`) writes a **temporary file** instead of a saved document, which
the model offers to the user as a download link (see "Downloading sandbox files");
without it the result is a new saved document. `format` (markdown|html) exists
where HTML input makes sense (all but `create_excel`, which takes Markdown
tables), and `landscape` where there are pages (`create_pdf`, `create_word`).

#### `create_word`
**Why:** produce a downloadable, editable Word document (`.docx`) written from
scratch. Same inputs as `create_pdf` (`content` or `source`; Markdown, or HTML
with `format: "html"`; `\newpage`; `landscape`; `scope`), same sanitising
(`mdpdf.Body`: scripts, forms, embeds, event handlers and external references
are removed and reported; only inline `data:` images survive). The page is
converted by headless LibreOffice (`soffice --convert-to docx`) inside a
bubblewrap sandbox (no network, empty environment, scratch directory only, file
size/CPU limits, one conversion at a time), then the page size is set to A4.
Output uses Word's own styles (Heading 1–3, lists, tables), not a house
stylesheet, so the user can restyle it. Saved like `save_doc`, with the Markdown
as its searchable text. Its source is kept, so the result can be changed later
with `edit_doc` (the file is rebuilt); an uploaded `.docx` is edited in place by `edit_doc` too. When LibreOffice or
`bwrap` is missing the tool stays offered and says Word creation is not set up.
Converter: `internal/server/mdword`; the `create-word` skill teaches the model.
**Example:** `create_word({"title": "Service agreement", "source": "tmp://contract.md"})`

#### `pdf_pages`
**Why:** make new PDFs out of pages of existing PDF documents — extract, split,
merge, reorder, drop pages — without re-typing anything: pages are copied as
they are by PDFium (`pdfextract.Assemble`), so layout, fonts and images are
kept. `outputs` is a list of files to create (up to 200 per call, so a PDF is
split into one file per person in a single step), each with a `title` and either
`pages` of the top-level `doc` (`"3,7,10-12"`, 1-based, in the order written;
`"all"`) or `parts` — `{doc, pages}` entries taken in order, for merging. All
outputs are validated first: a missing page or a duplicate title creates
nothing. Each new PDF is saved like `create_pdf`'s and indexed from its own page
text (so it is searchable with page numbers); scanned pages are copied but have
no text to index. Sources must be PDF documents with a stored file and are not
changed. The `pdf-pages` skill teaches the model the lookup → cut → (mail)
workflow.
**Examples:**
- `pdf_pages({"doc": "u.12", "outputs": [{"title": "Payslip Ana Horvat", "pages": "14,15"}, {"title": "Payslip Ivan Kovač", "pages": "31"}]})`
- `pdf_pages({"outputs": [{"title": "Contract with annexes", "parts": [{"doc": "u.20"}, {"doc": "u.21", "pages": "2-5"}]}]})`

#### `edit_pdf`
**Why:** annotate a PDF — highlight, underline, strike out, squiggle or box
passages and attach comments or sticky notes — without touching its pages or
text. The marks are standard PDF annotations (PDFium, `pdfextract.Annotate`)
that any viewer shows, edits and prints. `marks` is a list (up to 200 per call);
each has a `kind` (`highlight`, `underline`, `strikeout`, `squiggly`, `box`,
`note`) and usually `text`, the phrase to mark: case-insensitive, matched within
one line, and **every** occurrence is marked unless `page` and/or `nth` narrow it.
Optional per mark: `comment` (the text shown on hover/click), `color` (a name or
`#RRGGBB`), and for a note without `text` a `page` plus `x`, `y` in points from
the page's top-left corner. The result lists, per mark, how many annotations were
made and where, and which phrases were not found; if nothing could be marked,
nothing is changed or saved. The `edit-pdf` skill teaches the model.
**Where the result goes** (like `edit_doc`): **in place by default** — a saved PDF
document keeps its id, title and scope, its stored file gets the marks, the
previous file is kept as `.prev` and `edit_doc` with `undo: true` restores it;
the indexed text is unchanged, so nothing is re-indexed. A sandbox PDF (`doc` is
`tmp://a.pdf`) is changed where it is. Optional out-of-place modes: `save_as` (a
title) saves a **new saved document** and leaves the original alone (project in a
project session, else personal; `scope` as in `create_pdf`); `save_to` (a sandbox
path such as `tmp://marked.pdf`, `overwrite` to replace) writes a **temporary
file** (see "Downloading sandbox files" below). A PDF made by `create_pdf` is
refused in place — it is rebuilt from its source text on every `edit_doc`, which
would drop the marks — so it needs `save_as` or `save_to`. Shared documents
follow `edit_doc`'s rule (owner/admin).
Scanned PDFs have no text to find; positioned notes work on them.
**Examples:**
- `edit_pdf({"doc": "u.12", "marks": [{"kind": "highlight", "text": "total amount", "comment": "check against the invoice"}, {"kind": "strikeout", "text": "draft", "page": 1}]})`
- `edit_pdf({"doc": "u.12", "save_as": "Contract (reviewed)", "marks": [...]})`
- `edit_pdf({"doc": "tmp://report.pdf", "save_to": "tmp://marked.pdf", "marks": [{"kind": "note", "page": 2, "x": 72, "y": 90, "comment": "Needs a source"}]})`

#### Downloading sandbox files
**Why:** the agent can hand the user a file it made in the sandbox. It writes the
path (`tmp://marked.pdf`) in its reply and the client shows a **download link**:
the web UIs turn `tmp://…` text, or a Markdown link to one, into a clickable
chip that downloads through `GET /api/v1/files/tmp/<path>`; the TUI saves it with
`/download tmp://marked.pdf [dest]`. Only the caller's own `tmp://` sandbox is
served (never `storage://` or other users' files), always as an attachment, and
types such as HTML are sent as opaque downloads. The files are temporary — the
retention sweep (`data_retention_days`) removes them — so anything the user should
keep belongs in a saved document (`copy_doc` without a target). File names
without spaces link best.

### Copy, move and delete

Organise documents across files, scopes and projects.

#### `copy_doc`
**Why:** copy a document or a sandbox file server-side, so the content never
passes through the model. `source` is a document id or a sandbox file. With
`target` (a sandbox path) the copy is a working file: a document is copied as its
text (for a PDF/DOCX/XLSX, its Markdown text) unless the target has the
original's extension (`tmp://a.pdf` for a PDF), which copies the original file
byte for byte; a binary extension that does not match (text into `.pdf`) is
refused. A file is copied byte for byte; an existing target is refused unless
`overwrite: true`. Without `target` the copy is a new
saved document: a document is duplicated exactly, original file and index
included (a PDF stays a PDF; no re-embedding), and a sandbox text file is saved
like `save_doc`. `title` names it (default "Copy of …" / the file name); `scope`
as for `save_doc`. The source is never changed.
**Examples:**
- `copy_doc({"source": "u.12", "target": "tmp://work.md"})`
- `copy_doc({"source": "u.12", "target": "tmp://contract.pdf"})` (the PDF itself)
- `copy_doc({"source": "u.12", "title": "Contract — draft 2"})`

#### `move_doc` (owner/admin only)
**Why:** file documents into the right project from chat. Takes a document the user can
reach — personal (`u.N`), this session's project (`p.N`) or another project of theirs
(`p<id>.N`); shared (`s.N`) documents are refused — and a target project (`3`, `p3` or its
exact name). Same engine as the Files menu's Move/Copy to project: text, chunks, search
index and the original file are copied (no re-embedding) under a new id; `mode: "move"`
(default) then deletes the original, copying first so a failure leaves a duplicate, never a
loss. Old references to a moved document stop working. One document per call. Both sides
get change events, and a `document_move`/`document_copy` audit entry is written.
**Example:** `move_doc({"doc": "u.9", "to_project": "Acme leasing"})`

#### `create_project` (owner/admin only)
**Why:** let an owner or admin have the assistant set up a workspace ("make a project
called Acme leasing"). Creates a project with the user as its first member, refuses an
empty or duplicate name (matched case-insensitively among the user's projects), and writes a
`project_create` audit entry. The session does not switch into the new project; the user
selects it in the project picker, and invitations stay in the Projects screen. Ordinary
users do not get the tool (they create projects in the web/TUI UI).
**Example:** `create_project({"name": "Acme leasing"})`

#### `delete_doc`
**Why:** permanently delete a document and all its chunks when the user has
clearly asked to; the model is told to resolve the id with `list_documents` and
confirm with `ask_user` when unsure. Personal documents: always; project
documents: from the current project; shared documents: owner/admin only.
**Example:** `delete_doc({"doc": "u.12"})`

---

## Scheduling

### `cron_create`
**Why:** set up recurring work for the user. `kind:"js"` runs a script with **no
AI** each time (cheap periodic checks, e.g. watching a page); `kind:"skill"` runs a
full agent turn. `spec` is a 5-field cron, a `@descriptor`, or `@every <dur>`.
A skill job can wear a hat (`hat`); it defaults to the hat worn in the session
that creates it, and the result names it. A run whose hat was deleted fails.
**Example:**
`cron_create({"name": "fzoeu", "spec": "@every 30m", "kind": "js", "target": "skill://web-monitor/lib/check.js", "input": "{\"name\":\"fzoeu\"}"})`

### `cron_list`
**Why:** see the user's scheduled jobs and their last run status.
**Example:** `cron_list({})`

### `cron_update`
**Why:** change a job's schedule/target or enable/disable it; only passed fields
change.
**Example:** `cron_update({"id": 3, "spec": "@daily", "enabled": false})`

### `cron_run`
**Why:** trigger a job immediately to test it or force a check (runs within a minute
if enabled).
**Example:** `cron_run({"id": 3})`

### `cron_delete`
**Why:** remove a job the user no longer wants.
**Example:** `cron_delete({"id": 3})`
