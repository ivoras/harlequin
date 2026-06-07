---
name: web-extractor
description: Watch a web page for changes or extract specific data from it. Use when the user wants to monitor a page ("tell me when there are new <items> on <url>", "check this page for new entries") or pull a value/list from a page repeatedly. Set up once with AI; then re-check with zero AI cost.
---

# Web data extraction & change watching

Locate the wanted data on a page ONCE (with AI), save *how* to find it, then re-check
forever with no AI in the loop. Repeat checks must NOT call WebFetchDOM or any model —
they only run saved JavaScript.

## Phase 1 — Set up a watch (do this once, AI-assisted)

1. **Discover where the data is.** Call **WebFetchDOM** with the `url` and a `grep` of a
   distinctive phrase near the data (a visible item title, a heading, a date). Each
   result carries a CSS `path`. If you have no phrase, call WebFetchDOM with just the
   `url` to see the structure, then grep or try a `selector`.

2. **Generalise the path to a selector that matches ALL the repeating items.** A grep
   `path` points at one item, e.g. `... > ul:nth-of-type(1) > li:nth-of-type(3) > a`.
   Drop the `:nth-of-type(...)` on the repeating element to match its siblings
   (`ul li a`), or use a class you saw (`ul.calls li a`). **Verify** by calling
   **WebFetchDOM** with `selector="<your selector>"` — it should return the whole list,
   not just one item.

3. **Pick a short slug `<name>`** for this watch (e.g. the site or topic).

4. **Save the recipe and a tiny parser** with **run_js** (inline `code`). The recipe is
   the saved "how"; the parser is the saved code that re-checks it:

   ```
   storage.write("<name>/recipe.json", JSON.stringify({
     url: "<url>", selector: "<selector>", label: "<human label>",
     lastSeen: [], lastChecked: ""
   }));
   storage.write("<name>/parser.js",
     'include("skill://web-extractor/lib/extract.js");\ncheckWatch("<name>");\n');
   println("saved <name>");
   ```

5. **Record the baseline** by running the check once (Phase 2). The first run lists all
   current items as the starting point; tell the user it is now being watched.

## Phase 2 — Check a watch (no AI, cheap, repeatable)

Run the saved parser by reference:

```
run_js({ script: "storage://<name>/parser.js" })
```

(equivalently: `run_js({ script: "skill://web-extractor/lib/check.js", args: { name: "<name>" } })`).

It fetches the page, extracts the items at the saved selector, diffs them against the
previous check, prints any `+` additions / `-` removals, and updates the saved state.
Relay the result to the user. Re-run whenever asked (or on a schedule). Do not involve
the model in extraction here.

## Notes
- Helpers: `skill://web-extractor/lib/extract.js` — `fetchDoc(url)`, `allTextAt(doc, sel)`,
  `attrAt(doc, sel, attr)`, `diffList(old, new)`, `loadRecipe/saveRecipe`, `checkWatch`.
- For a single value (not a list), use a selector matching the one element and read
  `allTextAt(doc, selector)[0]`.
- For pages needing custom logic, write your own `storage://<name>/parser.js` using the
  helpers instead of the 2-line template.
- List saved watches: `run_js({ code: 'println(storage.list("*/recipe.json").join("\\n"))' })`.
- Sandbox JS is ES5 only: `var` (no `let`/`const`), no arrow functions, no template literals.
```
