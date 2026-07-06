---
name: compare-versions
description: Compare two revisions of the SAME document (old draft vs new draft, v1 vs v2, last year's policy vs this year's) and report what changed, was added, or was removed. load_skill this whenever asked what changed between document versions, to diff two revisions, or to review a new draft against the old one. For two DIFFERENT documents about the same subject (e.g. two countries' laws), load compare-topics instead.
---

# Compare two versions of a document

Goal: given an **old** and a **new** revision of the same text, produce a change report. The `align_docs` tool does the comparison mechanics — you only analyse the section pairs it returns, one batch at a time.

Rules:
- Report only differences visible in the pair texts; never guess or use outside knowledge.
- Quote numbers, amounts, dates and deadlines exactly as written in the pair.
- Cite the `[d.x.N]` id that appears DIRECTLY BEFORE the text you are quoting (each paragraph in a pair carries its own id). Never cite an id you did not see next to the quoted text. Every quoted excerpt needs its OWN citation directly after it — when summarizing several similar clauses together, do not let citations trail off after the first one; `save_doc` checks this and will refuse to save if a quote is uncited or cited to the wrong chunk.
- Never describe the CONTENT of a section beyond its heading unless you actually fetched that pair with view="pairs" this turn. Seeing a heading in the summary tells you it exists and roughly what it's about — it tells you nothing about its rules, amounts, or procedures. If you didn't read it, either fetch it or leave it as a one-line heading mention (ADDED/REMOVED), not a described finding.
- A side marked CLIPPED has undisplayed text: never claim something is new or removed based on a clipped side.
- **Mandatory before writing "new", "removed", "no longer exists", "only in the new/old version", or any ADDED/REMOVED finding whose wording is specific (an amount, a rule, a named body) rather than just a heading:** call `check_text` on the OTHER document with the key phrase or term. `align_docs` classifying a section as only_a/only_b is a hint, not proof — headings get renamed, split across chunks, or duplicated by the source PDF's own conversion, and the true counterpart can still exist under different wording. Only assert novelty after `check_text` comes back "not found". If it finds a hit, that is the real counterpart — read it and report a CHANGED/RENAMED finding instead, not ADDED/REMOVED.
- Never claim WHERE removed content went or WHY it changed unless the new document's text says so explicitly. A removed section and an unrelated added section are two separate findings — do not narrate one as having become, merged into, or been absorbed by the other just because both appear near the same area. If you did not read text stating the connection, do not state it.
- Some pairs differ only in wording or chunk boundaries. If both sides mean the same thing, record "no substantive difference" and move on — do not invent a difference.

## Step 1 — Identify the two documents

You need two scoped document ids (`u.N` personal, `s.N` shared, `p.N` project).

- If the user gave ids, use them.
- Otherwise call `list_documents` and match the user's wording against each document's title AND description (titles are often raw filenames; the description says what the document is).
- Decide which revision is **old** and which is **new** (from names, descriptions or dates like "v2", "draft", "2026"). If you cannot tell, `ask_user`.

## Step 2 — Get the alignment summary

Call:

    align_docs({"doc_a": "<old id>", "doc_b": "<new id>", "mode": "versions"})

A is always the old revision, B the new one. The summary lists: how many sections are identical, the paired sections that differ (most different first, each with a pair number `#N`), sections **Only in A** (removed) and **Only in B** (added).

The summary alone gives the structural findings:
- every "Only in A" heading → `REMOVED: <heading>`
- every "Only in B" heading → `ADDED: <heading>`
- renamed headings in paired lines (`A: x ↔ B: y`) → `RENAMED: x → y`

## Step 3 — Drill into the content changes that matter

First decide the question's SCOPE from the summary's headings:

**Narrow** (one article, one specific named change, or "what changed overall" with no particular topic): read only the most-different pairs (lowest similarity) and anything matching the user's wording. HARD LIMIT: at most 6 pairs across at most 3 pairs-view calls. Choose the pair numbers BEFORE the first call:

    align_docs({"doc_a": "...", "doc_b": "...", "mode": "versions", "view": "pairs", "cursor": <#N from the summary>})

or filter instead of cursor:

    align_docs({..., "view": "pairs", "filter": "grant rate"})

**Thematic** (the question names a topic that plausibly spans many headings — e.g. "funding/subsidies/financial aid", "oversight/control/revision", "which bodies were added or removed", "how are operations simplified"): scan EVERY heading in the summary (paired, only-A, only-B) and pick every pair number whose heading plausibly relates to the theme — this is commonly 10-30 pairs for an 80-page document, not 6. Fetch them together in one or two calls, NOT one call per pair:

    align_docs({..., "view": "pairs", "pairs": "9,12,16,19-22,44,51"})

`filter` also accepts comma-separated OR terms for a keyword-driven theme sweep: `filter: "grant,subsidy,funding,co-financing,flat rate"`. Use whichever selection method covers the theme better; both return multiple pairs per call.

For each pair write ONE finding line: does the meaning change (an obligation, amount, date, scope, or permission)?
- Yes → `CHANGED [cites]: <what the old said> → <what the new says>` (quote values exactly).
- No → `SAME [cites]: wording only`.
- If the pair is irrelevant to a thematic question after all (a false hit from the keyword/number sweep), skip it — don't force a finding.

## Step 4 — Report

Write a Markdown report from your finding lines and the summary (and nothing else):

1. **Summary** — 2-4 sentences: how much changed overall (use the identical count), and the most consequential changes.
2. **Substantive changes** — numbered list of the CHANGED findings, old → new, with citations.
3. **Added** — the ADDED headings (one line each; you don't need to read their text unless asked).
4. **Removed** — the REMOVED headings.
5. **Renamed/restructured** — the RENAMED lines.

If there were no pairs at all, say the revisions are identical section for section. If the user asked a narrower question, lead with the findings on that topic and keep the rest brief.

## Step 5 — Save the report (default ON)

Unless the user said **not** to save it, persist the report so later questions about this comparison can be answered from it:

    save_doc({"title": "Comparison: <old title> vs <new title>", "content": "<the full report from Step 4>"})

Do NOT pass a scope — the default is correct (the project corpus in a project session, else personal).

Keep the `[d.x.N]` citations in the saved content — they stay linked to the source documents. Then tell the user the report was saved and that they can ask follow-up questions about it later.
