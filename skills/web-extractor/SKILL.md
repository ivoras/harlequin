---
name: web-extractor
description: Monitor a web page for changes or new items (e.g. "tell me when there are new <items> on <url>", "check this page every N hours"). Set up once with AI, then re-check on a schedule with zero AI cost. load_skill this whenever the user asks to watch/monitor a page.
---

# Watch a web page for changes

Find the list of items ONCE with **WebFetchDOM**, then schedule a **cron** job that
re-checks it with no AI. Do this in three tool calls. Do NOT explore the page with
`run_js` — use `WebFetchDOM`.

## Step 1 — Find the selector for the list

Call **WebFetchDOM** with only the `url`. It returns **candidate lists**, each with a
`selector`, a `count`, and a `sample`. Choose the candidate whose `sample` matches the
items the user wants (e.g. the subsidy-call titles) and take its `selector`.

- To confirm, call **WebFetchDOM** again with `selector="<candidate>"` — it should
  return the full list of items.
- If no candidate fits, call **WebFetchDOM** with `grep="<a few words from one item>"`
  and build a selector from the returned node's `class` (as `tag.class`) or use its `path`.

The selector must match EVERY item (the repeating row), not just one.

## Step 2 — Schedule the watch

Call **cron_create** (kind `js`), pointing at the shipped checker and passing the watch
config as `input` (a JSON string):

    cron_create(
      name:   "<short-slug>",
      spec:   "<schedule>",
      kind:   "js",
      target: "skill://web-extractor/lib/check.js",
      input:  "{\"name\":\"<short-slug>\",\"url\":\"<url>\",\"selector\":\"<selector>\",\"label\":\"<what is watched>\"}"
    )

Schedules: 5-field cron, `@hourly`/`@daily`, or `@every <dur>`. **Every 12 hours →
`"0 0,12 * * *"`** (00:00 and 12:00), or `"@every 12h"`.

The first run saves the current items as the baseline; later runs report only
additions/removals, and the user is notified when the list changes.

## Step 3 — Baseline now (optional) and confirm

Call **cron_run** with the new job's id to run it immediately and capture the baseline
(otherwise it first runs at the next scheduled time). Then tell the user what is being
watched, on what schedule, and how many items are there now.

## Notes
- Re-check a saved watch by hand:
  `run_js({ script: "skill://web-extractor/lib/check.js", args: { name: "<slug>" } })`.
- Helpers in `skill://web-extractor/lib/extract.js`: `fetchDoc`, `allTextAt`,
  `attrAt`, `diffList`, `runWatch`. Sandbox JS is ES5 only.
