---
name: web-extractor
description: Monitor a web page for changes or new items (e.g. "tell me when there are new <items> on <url>", "check this page every N hours"). Set up once with AI, then re-check on a schedule with zero AI cost. load_skill this whenever the user asks to watch/monitor a page.
---

# Watch a web page for changes

Find what to watch ONCE with **WebFetchDOM**, then schedule a **cron** job that
re-checks it with no AI.

**Use only `WebFetchDOM` to find and confirm the selector — never `run_js` for this.**
WebFetchDOM saves the page to a `tmp://…​.html` handle; that file is **data, not a
script**. Do NOT pass it to `run_js` as `script` (that runs HTML as JS and fails with
"Unexpected token <"). You do not need `run_js` at all to set up a watch.

## Step 1 — Find the selector for the list

Call **WebFetchDOM** with only the `url`. It returns **candidate lists**, each with a
`selector`, a `count`, and a `sample`. Choose the candidate whose `sample` matches the
items the user wants (e.g. the subsidy-call titles) and take its `selector`.

- To confirm, call **WebFetchDOM** again with `selector="<candidate>"` — it should
  return the full list of items.
- If no candidate fits, call **WebFetchDOM** with `grep="<a few words from one item>"`
  and build a selector from the returned node's `class` (as `tag.class`) or use its `path`.

The selector must match EVERY item (the repeating row), not just one.

## Step 2 — Decide where to notify

A change can be delivered via **inapp** (the built-in TUI/web notification),
**email**, or **telegram**. Pick the channel:
- If the user already said where ("email me", "send it to Telegram"), use that.
- Otherwise call **notify_channels** to see what's available for this user, then
  **ask_user** which one they want (only offer the channels it returns — e.g. don't
  offer Telegram if it isn't configured). Default is `inapp` if they don't care.

## Step 3 — Schedule the watch

Call **cron_create** (kind `js`), pointing at the shipped checker, passing the watch
config as `input` (a JSON string) and the chosen `notify_channel`:

    cron_create(
      name:           "<short-slug>",
      spec:           "<schedule>",
      kind:           "js",
      target:         "skill://web-extractor/lib/check.js",
      input:          "{\"name\":\"<short-slug>\",\"url\":\"<url>\",\"selector\":\"<selector>\",\"label\":\"<what is watched>\"}",
      notify_channel: "<inapp|email|telegram>"
    )

Schedules: 5-field cron, `@hourly`/`@daily`, or `@every <dur>`. **Every 12 hours →
`"0 0,12 * * *"`** (00:00 and 12:00), or `"@every 12h"`.

The first run saves the current items as the baseline; later runs report only
additions/removals, and the user is notified (on the chosen channel) when the list
changes. Transient fetch failures do not notify.

## Step 4 — Baseline now (optional) and confirm

Call **cron_run** with the new job's id to run it immediately and capture the baseline
(otherwise it first runs at the next scheduled time). Then tell the user what is being
watched, on what schedule, where they'll be notified, and how many items are there now.

## Notes
- Re-check a saved watch by hand:
  `run_js({ script: "skill://web-extractor/lib/check.js", args: { name: "<slug>" } })`.
- Helpers in `skill://web-extractor/lib/extract.js`: `fetchDoc`, `allTextAt`,
  `attrAt`, `diffList`, `runWatch`. Sandbox JS runs on goja (ES5.1-compatible;
  common ES6 like let/const, arrow functions and template literals also works).
- Prefer `target="skill://web-extractor/lib/check.js"` over hand-written inline JS.
  If you ever do write inline JS for a cron job, it is the **body** of a function the
  runtime wraps for you: write top-level statements (top-level `return` is fine) and
  do **not** wrap it in `function(){...}` — an un-called function runs nothing, and
  `function() {` is a syntax error.
