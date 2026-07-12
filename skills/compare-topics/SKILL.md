---
name: compare-topics
description: Compare two DIFFERENT documents about the same subject (two countries' laws on one topic, two vendors' contracts, two policies) and report how they treat each topic and what only one of them covers. load_skill this whenever asked to compare two distinct documents, laws, contracts or policies against each other. NOT for old/new, previous/current, or original/amended pairs — two versions of the same text are compare-versions territory even when the user calls them "two documents"; load compare-versions for those.
---

# Compare two documents about the same subject

Goal: given two different texts on the same subject, report topic by topic how they agree, how they differ, and what each covers that the other does not. The `align_docs` tool pairs sections by meaning — the documents may even be in different languages; compare meaning, and write the report in the user's language.

Rules:
- Report only what the pair texts say; never guess or use outside knowledge of either document's country, vendor or subject.
- Quote amounts, durations, deadlines and thresholds exactly as written.
- Cite the `[d.x.N]` id that appears DIRECTLY BEFORE the text you are quoting (each paragraph in a pair carries its own id). Never cite an id you did not see next to the quoted text. Every quoted excerpt needs its OWN citation directly after it — when summarizing several similar clauses together, do not let citations trail off after the first one; `save_doc` checks this and will refuse to save if a quote is uncited or cited to the wrong chunk.
- Never describe the CONTENT of a section beyond its heading unless you actually fetched that pair with view="pairs" this turn. Seeing a heading in the summary tells you it exists and roughly what it's about — it tells you nothing about its rules, amounts, or procedures. If you didn't read it, either fetch it or leave it as a one-line heading mention, not a described finding.
- A side marked CLIPPED has undisplayed text: never claim a document lacks something based on a clipped side.
- **Mandatory before writing "only in A/B", "not covered", "silent on", or any finding whose wording is specific (an amount, a rule, a named body) rather than just a heading:** call `check_text` on the OTHER document with the key phrase or term. `align_docs` classifying a section as only_a/only_b is a hint, not proof — headings get renamed or reworded, and the true counterpart can still exist under different wording. Only assert the gap after `check_text` comes back "not found". If it finds a hit, that is the real counterpart — read it and report a DIFFERS/EQUIVALENT finding instead.
- Never claim WHERE removed content went or WHY it changed unless the text says so explicitly. A removed section and an unrelated added section are two separate findings — do not narrate one as having become, merged into, or been absorbed by the other just because both appear near the same area. If you did not read text stating the connection, do not state it.
- Sections with no counterpart (`only_a` / `only_b`) are findings, not noise: they show what one document regulates and the other is silent about.

## Step 1 — Identify the two documents

You need two scoped document ids (`u.N` personal, `s.N` shared, `p.N` project).

- If the user gave ids, use them.
- Otherwise call `list_documents` and match the user's wording against each document's title AND description (titles are often raw filenames; the description says what the document is).
- Note which real-world thing each id stands for (e.g. A = the Croatian law, B = the German law) and keep calling them by those names in findings and the report.

## Step 2 — Get the alignment summary

Call:

    align_docs({"doc_a": "<first id>", "doc_b": "<second id>", "mode": "topical"})

The summary lists the matched section pairs (most different first, each with a pair number `#N`) and the sections found in only one document. If it pairs almost nothing, call it once more with `"min_similarity": 0.45`; if it pairs unrelated sections, use `0.65`. Then keep that value.

The orphan lists are findings by themselves:
- "Only in A" heading → `ONLY A: <heading>`
- "Only in B" heading → `ONLY B: <heading>`

## Step 3 — Read the matched pairs that matter

First decide the question's SCOPE from the summary's headings:

**Narrow** (one topic or article, or "how do these compare overall" with few matched pairs): fetch all matched pairs when there are few, otherwise the most-different ones and those relevant to the user's question. HARD LIMIT: at most 8 pairs across at most 4 pairs-view calls. Choose the pair numbers BEFORE the first call:

    align_docs({..., "view": "pairs", "cursor": <#N>})       — or —
    align_docs({..., "view": "pairs", "filter": "<topic or article number>"})

**Thematic** (the question names a theme spanning many headings — e.g. "financial provisions", "oversight bodies", "enforcement mechanisms"): scan every heading in the summary and pick every pair number relating to the theme — this can be many more than 8 for a long document. Fetch them together in one or two calls, NOT one call per pair:

    align_docs({..., "view": "pairs", "pairs": "3,7,11-14,22"})

`filter` also accepts comma-separated OR terms for a keyword sweep, e.g. `filter: "audit,inspection,oversight,monitoring"`.

For each pair write ONE finding line:
- They impose the same rule → `EQUIVALENT <topic> [cites]: <one sentence>`.
- They differ → `DIFFERS <topic> [cites]: A <what A says>; B <what B says>` (quote the key values; note which side is stricter if clear).
- The pairing is spurious (not the same topic) → `SPURIOUS [cites]` and move on.

## Step 4 — Report

Write a Markdown report from your finding lines (and nothing else), using the documents' real names, not "A"/"B":

1. **Summary** — 2-4 sentences: overall how similarly the two documents treat the subject, and the sharpest differences.
2. **Key differences** — numbered list of the DIFFERS findings: topic, then each document's position, with citations.
3. **Common ground** — the EQUIVALENT findings, one line each.
4. **Only in <document A's name>** — the ONLY A findings.
5. **Only in <document B's name>** — the ONLY B findings.

Ignore SPURIOUS findings in the report (they are alignment noise). If the user asked a narrower question (e.g. only about penalties), lead with the findings on that topic.

## Step 5 — Save the report (default ON)

Unless the user said **not** to save it, persist the report so later questions about this comparison can be answered from it:

    save_doc({"title": "Comparison: <document A's name> vs <document B's name>", "content": "<the full report from Step 4>"})

Do NOT pass a scope — the default is correct (the project corpus in a project session, else personal).

Keep the `[d.x.N]` citations in the saved content — they stay linked to the source documents. Then tell the user the report was saved **and state its scoped id from the save_doc result** (e.g. "saved as **p.21**") — the clients render that id as a clickable link that opens the report, so never omit it — and that they can ask follow-up questions about it later.
