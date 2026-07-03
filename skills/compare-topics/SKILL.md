---
name: compare-topics
description: Compare two DIFFERENT documents about the same subject (two countries' laws on one topic, two vendors' contracts, two policies) and report how they treat each topic and what only one of them covers. load_skill this whenever asked to compare two distinct documents, laws, contracts or policies against each other. For two revisions of the same document, load compare-versions instead.
---

# Compare two documents about the same subject

Goal: given two different texts on the same subject, report topic by topic how they agree, how they differ, and what each covers that the other does not. The `align_docs` tool pairs sections by meaning — the documents may even be in different languages; compare meaning, and write the report in the user's language.

Rules:
- Report only what the pair texts say; never guess or use outside knowledge of either document's country, vendor or subject.
- Quote amounts, durations, deadlines and thresholds exactly as written.
- Cite the chunk ids shown with each pair, e.g. `[d.p.9]`, after every finding.
- Sections with no counterpart (`only_a` / `only_b`) are findings, not noise: they show what one document regulates and the other is silent about.

## Step 1 — Identify the two documents

You need two scoped document ids (`u.N` personal, `s.N` shared, `p.N` project).

- If the user gave ids, use them.
- Otherwise `search_docs` for each document's title; results show the id and scope (e.g. `doc 4`, scope `shared` → `s.4`).
- Note which real-world thing each id stands for (e.g. A = the Croatian law, B = the German law) and keep calling them by those names in findings and the report.

## Step 2 — Align

Call:

    align_docs({"doc_a": "<first id>", "doc_b": "<second id>", "mode": "topical"})

If the result pairs almost nothing (nearly everything only_a/only_b), call it once more with `"min_similarity": 0.45`; if it pairs unrelated sections, use `0.65`. Then keep that value for every batch.

## Step 3 — Analyse each pair in the batch

Write ONE finding line per pair:

- `matched` pair → compare the two sections on their shared topic:
  - They impose the same rule → `EQUIVALENT <topic> [cites]: <one sentence>`.
  - They differ → `DIFFERS <topic> [cites]: A <what A says>; B <what B says>` (quote the key values; note which side is stricter if clear).
  - The pairing is spurious (the sections are not about the same thing) → `SPURIOUS [cites]` and move on.
- `only_a` pair → `ONLY A <topic> [cites]: <one-sentence summary>`
- `only_b` pair → `ONLY B <topic> [cites]: <one-sentence summary>`

Keep each finding to one line; you will need them all in Step 5.

## Step 4 — Next batch

If the result says to call again with a cursor, repeat Steps 3-4 with the same arguments plus `"cursor": <N>` until the result says it is the last batch. Do not stop early and do not skip cursors.

## Step 5 — Report

Write a Markdown report from your finding lines (and nothing else), using the documents' real names, not "A"/"B":

1. **Summary** — 2-4 sentences: overall how similarly the two documents treat the subject, and the sharpest differences.
2. **Key differences** — numbered list of the DIFFERS findings: topic, then each document's position, with citations.
3. **Common ground** — the EQUIVALENT findings, one line each.
4. **Only in <document A's name>** — the ONLY A findings.
5. **Only in <document B's name>** — the ONLY B findings.

Ignore SPURIOUS findings in the report (they are alignment noise). If the user asked a narrower question (e.g. only about penalties), lead with the findings on that topic.
