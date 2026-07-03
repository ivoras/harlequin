---
name: compare-versions
description: Compare two revisions of the SAME document (old draft vs new draft, v1 vs v2, last year's policy vs this year's) and report what changed, was added, or was removed. load_skill this whenever asked what changed between document versions, to diff two revisions, or to review a new draft against the old one. For two DIFFERENT documents about the same subject (e.g. two countries' laws), load compare-topics instead.
---

# Compare two versions of a document

Goal: given an **old** and a **new** revision of the same text, produce a change report. The `align_docs` tool does the comparison mechanics — you only analyse the section pairs it returns, one batch at a time.

Rules:
- Report only differences visible in the pair texts; never guess or use outside knowledge.
- Quote numbers, amounts, dates and deadlines exactly as written in the pair.
- Cite the chunk ids shown with each pair, e.g. `[d.s.14]`, after every finding.
- Some pairs differ only in wording or chunk boundaries. If both sides mean the same thing, record "no substantive difference" and move on — do not invent a difference.

## Step 1 — Identify the two documents

You need two scoped document ids (`u.N` personal, `s.N` shared, `p.N` project).

- If the user gave ids, use them.
- If the user gave titles or described the documents, `search_docs` for each title; results show the document id and scope (e.g. `doc 12`, scope `project` → `p.12`).
- Decide which revision is **old** and which is **new** (from names like "v2", "draft", "2026", or by asking). If you cannot tell, `ask_user`.

## Step 2 — Align

Call:

    align_docs({"doc_a": "<old id>", "doc_b": "<new id>", "mode": "versions"})

A is always the old revision, B the new one. The result reports how many identical sections were skipped and returns the first batch of differing pairs.

## Step 3 — Analyse each pair in the batch

Write ONE finding line per pair, in this format:

- `changed` pair → decide: does the meaning change (an obligation, amount, date, scope, or permission)? 
  - Yes → `CHANGED [cites]: <what the old said> → <what the new says>` (one sentence each side).
  - No → `SAME [cites]: wording only`.
- `only_a` pair → `REMOVED [cites]: <one-sentence summary of the dropped text>`
- `only_b` pair → `ADDED [cites]: <one-sentence summary of the new text>`

Keep each finding to one line; you will need them all in Step 5.

## Step 4 — Next batch

If the result says to call again with a cursor, repeat Steps 3-4:

    align_docs({"doc_a": "...", "doc_b": "...", "mode": "versions", "cursor": <N>})

Continue until the result says it is the last batch. Do not stop early and do not skip cursors.

## Step 5 — Report

Write a Markdown report from your finding lines (and nothing else):

1. **Summary** — 2-4 sentences: how much changed overall (use the identical-sections count), and the most consequential changes.
2. **Substantive changes** — numbered list of the CHANGED findings, old → new, with citations.
3. **Added** — the ADDED findings.
4. **Removed** — the REMOVED findings.
5. **Wording-only** — a single line: how many pairs differed only in wording.

If there were no pairs at all, say the revisions are identical section for section.

## Step 6 — Save the report (default ON)

Unless the user said **not** to save it, persist the report so later questions about this comparison can be answered from it:

    save_doc({"title": "Comparison: <old title> vs <new title>", "content": "<the full report from Step 5>"})

Keep the `[d.x.N]` citations in the saved content — they stay linked to the source documents. Then tell the user the report was saved and that they can ask follow-up questions about it later.
