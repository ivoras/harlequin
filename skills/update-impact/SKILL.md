---
name: update-impact
description: Assess whether a document needs updating after another document changed — given a change/comparison report (or an old and new revision) and a target document, find which parts of the target are affected and need editing. load_skill this whenever asked "does X need updating", "is our policy still correct after the new regulation", "which parts of X are outdated", or to propagate changes from one document into another. For producing the change report itself, load compare-versions (same document, two revisions) or compare-topics (two different documents) first.
---

# Does a document need updating after a change?

Goal: given **what changed** (a saved comparison report, or an old+new revision
pair) and a **target document** (a policy, contract, manual, or summary that was
written against the old state), classify each change as affecting the target or
not, and point at the exact target sections that need editing.

Rules:
- Report only what the fetched texts show; never guess from titles, headings, or
  outside knowledge.
- Every claim about the target document needs a `[d.x.N]` citation to the target
  chunk it came from, and every claim about a change needs the report's (or
  revisions') citation. An AFFECTED finding therefore carries citations to BOTH
  sides.
- **Never classify a change as UNAFFECTED from search silence alone.**
  `search_docs` is ranked retrieval and can miss; before writing UNAFFECTED,
  `check_text` the target for the change's key term(s) — the old value, the
  renamed body, the amount — and only assert UNAFFECTED after "not found".
- The decisive AFFECTED signal is the target still stating the OLD rule: run
  `check_text` on the target with the superseded value or name (e.g. the old
  fee cap, the abolished committee). A hit is proof, and its citation is the
  edit location.
- Do not draft the corrected text unless the user asks; the deliverable is the
  impact report. If asked to also propose edits, quote the current target text
  and the new rule side by side, both cited.
- Quote numbers, amounts, dates and names exactly as written.

## Step 1 — Identify the inputs

You need (a) the change source and (b) the target document, as scoped ids
(`u.N`, `s.N`, `p.N`, or `p<project>.N` for another project's document).

- **Change source**: prefer a saved comparison report (`list_documents` — save_doc
  reports are usually titled "Comparison: …"). If none exists but the user named
  two revisions, produce one first with the compare-versions flow (align_docs
  mode "versions"), save it, then continue here.
- **Target**: resolve from the user's wording via `list_documents` (match the
  description, not just the title). If several documents plausibly match,
  `ask_user`.

## Step 2 — Pair the report's changes with the target's sections

    align_docs({"doc_a": "<report id>", "doc_b": "<target id>", "mode": "topical"})

The summary pairs each change item in the report with the target sections about
the same subject. Fetch the paired sections (view="pairs", batching pair numbers
like compare-versions does) — each pair shows a change next to the target text
that may embody the old rule.

Report sections with NO target counterpart in the summary are only *candidates*
for UNAFFECTED — confirm in Step 3.

## Step 3 — Verify each change against the target

For every substantive change in the report, in pair order:

1. Extract its key terms: the old value, the new value, and the topic's
   distinctive words (both old and new names when something was renamed).
2. `check_text` the target for the OLD value/name.
   - Found → **AFFECTED**: the target still states the superseded rule. Cite the
     target chunk (the edit location) and the report chunk (the reason).
3. If the old value is absent, `check_text` for the topic term; also use the
   Step 2 pair text.
   - The target discusses the topic but with wording that neither matches the
     old nor reflects the new rule → **REVIEW**: a human must judge; cite the
     closest target chunk and say what to compare.
   - "not found" for both old value and topic → **UNAFFECTED** (state which
     check_text probes came back empty).

## Step 4 — Report

1. **Verdict** — one sentence: does the target need updating, and how much
   (e.g. "3 sections must change, 2 need review, 9 changes don't touch it").
2. **Must update** — numbered AFFECTED findings: target section (cited) →
   what it currently says → what changed (cited) → what the edit must reflect.
3. **Review** — the REVIEW findings with what to compare.
4. **Not affected** — one line per UNAFFECTED change, with the probe terms that
   came back "not found".

## Step 5 — Save the report (default ON)

Unless the user said not to save it:

    save_doc({"title": "Update impact: <target title> vs <report title>", "content": "<the full report>"})

Do NOT pass a scope — the default is correct. Keep all `[d.x.N]` citations in
the saved content. Tell the user it was saved, **state its scoped id from the
save_doc result** (e.g. "saved as **p.22**" — the clients render that id as a
clickable link that opens the report, so never omit it), and that they can ask
to draft the actual edits next.
