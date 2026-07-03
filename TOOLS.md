# Built-in tools

These are the tools the Harlequin agent exposes to the LLM during a turn. The
model calls them by name with JSON arguments; results are fed back into the loop.
Some tools are only present when the corresponding feature is enabled in
`server.yaml` (noted per tool). Defined in `internal/server/agent/tools.go`.

Beyond these built-ins, two families of tools appear dynamically:
- **Skill tools** — namespaced `<skill>.<tool>`, declared by a skill's frontmatter
  and backed by sandboxed JS. Visible per the active hat/skill set.
- **MCP tools** — namespaced `mcp__<server>__<tool>`, proxied from external MCP
  servers (only when `mcp.enabled`).

---

## Memory

### `memory_search`
**Why:** recall durable facts/preferences about the user and their org before
answering, so the agent stays consistent across sessions.
**Example:** `memory_search({"query": "preferred currency"})`
→ ranked hits, each with a composite id (`u.N`/`s.N`) and `slot_key` to use with
`memory_change`/`memory_delete`.

### `memory_write`
**Why:** persist a new durable fact. `scope` is `user` (personal, default) or
`shared` (org-wide, owner/admin only). An optional `slot_key` files the fact under
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

---

## Skills

### `list_skills`
**Why:** discover available skills (name + description) before loading one.
**Example:** `list_skills({})`

### `load_skill`
**Why:** pull a skill's full instructions and resource files into context, on
demand, so they don't bloat every prompt.
**Example:** `load_skill({"name": "web-monitor"})`

---

## Compute & code

### `calculator`
**Why:** do exact arithmetic instead of guessing; LLMs are unreliable at math.
ES5.1+ expression syntax, `Math` available.
**Example:** `calculator({"expression": "(1500 * 1.08).toFixed(2)"})` → `"1620.00"`

### `run_js`
**Why:** general escape hatch — run JavaScript in a sandbox for logic, parsing,
HTTP, and per-user file storage that the model can't do itself. The engine (goja)
is **ES5.1-compatible and supports much of ES6** (let/const, arrow functions,
template literals, classes, destructuring, spread/rest, `for…of`, generators,
Map/Set, Symbol, Promise, typed arrays, BigInt); emit output with `println()`/`print()`.
Helpers: `fetch(url)`, `dom.parse/query/grep/json`, per-user `tmp.*`/`storage.*`
stores, and `load(uri)`/`include(uri)` for `skill://`/`storage://`/`tmp://` scripts.
**Examples:**
- inline: `run_js({"code": "var r = fetch('https://example.com'); println(r.status);"})`
- saved script + args: `run_js({"script": "skill://web-monitor/lib/check.js", "args": {"name": "fzoeu"}})`

---

## Web (only when `agent.web_fetch.enabled`)

### `WebFetch`
**Why:** read a web page and get an AI answer about it — fetches the URL, converts
HTML→Markdown, and analyses it with a small model. Best for "what does this page
say" questions. (15-min cache; reports cross-host redirects to call again.)
**Example:** `WebFetch({"url": "https://example.com/pricing", "prompt": "What are the plan tiers and prices?"})`

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

## Documents (only when the document corpus is configured)

### `search_docs`
**Why:** retrieve passages from the organisation's RAG document corpus (uploaded
PDFs/text) to ground answers in source material.
**Example:** `search_docs({"query": "OTC derivatives reporting obligations"})`

### `align_docs`
**Why:** compare two corpus documents without holding both in context. The
server aligns their sections deterministically — mode `versions` diffs two
revisions of the same text (identical sections are skipped), mode `topical`
pairs sections of two different texts about the same subject by embedding
similarity (leftovers are reported as present in only one document) — and
returns the pairs in cursor batches for the model to analyse one at a time.
**Example:** `align_docs({"doc_a": "p.3", "doc_b": "p.4", "mode": "topical"})`,
then again with `"cursor": 5` etc. until the last batch.

---

## Scheduling (only when `cron` is enabled)

### `cron_create`
**Why:** set up recurring work for the user. `kind:"js"` runs a script with **no
AI** each time (cheap periodic checks, e.g. watching a page); `kind:"skill"` runs a
full agent turn. `spec` is a 5-field cron, a `@descriptor`, or `@every <dur>`.
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
