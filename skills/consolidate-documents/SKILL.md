---
name: consolidate-documents
description: >-
  Produce ONE new, consolidated document by applying amendments/updates to a base document — a law and the act that amends it, a contract and its addenda, a policy and its later revisions — and save it. load_skill this whenever asked to "combine", "merge", "consolidate" or "integrate" a document with its amendments/changes, to "make the current/updated version", or to "create a new document that includes both". Works in context from the fetched texts, with no structure analysis. NOT for reporting what changed (use compare-versions) or for checking whether another document needs updating (use update-impact).
---

# Consolidate a document with its amendments

Goal: one new document = the base text with every amendment applied, saved to
the corpus. Do the work **in context, from the texts you fetched**. Do not
analyse the structure first.

Rules:
- **No preliminary analysis.** Do not parse the HTML, count articles, explore the
  DOM, list headings, `run_js` the sources, search the web for other versions, or
  deliberate about edge cases before starting. The texts are in your context;
  read the amendment and act on it.
- **Fetch every source exactly once.** Whatever you fetched is in your context —
  never fetch the same URL again, with any tool.
- **Do not ask the user** unless a source cannot be fetched or no amendment
  instruction can be matched to the base at all.
- Never invent or rephrase legal/contract text. Copy replaced and inserted text
  exactly as the amendment gives it. Keep the sources' language.
- An instruction that cannot be applied (its target text is not in the base, for
  instance because an earlier amendment created it) is **skipped, not an
  obstacle**: note it and continue.

## Step 1 — Fetch (once each, in one response)

Call `MarkdownFetch` for the base and for each amendment in the same response.
For the **base** pass `save_file`, e.g. `{"url": "<base url>", "save_file": "base.md"}`:
the full text is then also a working file, `tmp://base.md`. Amendments need no
`save_file`. If a source is a file already in the corpus (`u.N` / `s.N` / `p.N`),
read it with `search_docs`/`list_documents` instead and start the working file
from it with `edit_file(append)`.

## Step 2 — Apply the amendments, in order

Read each amendment's instructions in the order the amending text gives them.
Typical forms and how to apply each to `tmp://base.md`:

| Instruction says | Do |
|---|---|
| replace/amend words, a sentence or a paragraph | `edit_file` with `old_string` (copied exactly from the base text in your context, with enough surrounding words to be unique) and `new_string` |
| insert a new article/section/paragraph after X | `edit_file`: `old_string` = the end of X, `new_string` = that same text + the new text |
| delete an article/section/words | `edit_file` with `new_string` empty (or a short "(repealed)" marker if the amendment says so) |
| a whole article is replaced by new text | `edit_file`: `old_string` = the article's old text, `new_string` = the new article |

Work efficiently — a step budget applies: put **many `edit_file` calls in one
response** (they run in order), and for runs of nearby changes one `diff` with
several hunks is fine. If an `old_string` is not found or not unique, correct it
from the text in your context and retry once; if it still fails, skip the
instruction and record it.

Do not renumber or reformat anything the amendment does not mention.

## Step 3 — Add the consolidation notes

Append (`edit_file` with `append`) a short final section titled "Consolidation
notes" listing: the base document and each amendment (title and publication
reference), how many instructions were applied, and every instruction that was
**not** applied with the reason (e.g. "Article 5: target article not present in
the base text"). If the base may not be the latest consolidated version of the
document, say so there.

## Step 4 — Save

    save_doc({"title": "<Base title> — consolidated with <amendment title>",
              "source": "tmp://base.md", "is_raw": true})

Leave `scope` out (the default — the project in a project session — is right).
`is_raw` is true because this is source text, not a report quoting the corpus.

## Step 5 — Report

Answer in a few lines: the saved document's id, how many instructions were
applied, the list of skipped ones with reasons, and the caveat about the base
version if it applies. Offer nothing else unless asked.
