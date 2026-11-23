---
name: edit-pdf
description: Mark up an existing PDF — highlight, underline, strike out or box passages, and attach comments or sticky notes (review a contract, flag figures in a report, mark what to fix, annotate for a colleague). load_skill this whenever the user asks to highlight, underline, mark, flag, comment on or annotate a PDF, or to "review" one and show where the issues are.
---

# Annotating a PDF

`edit_pdf` adds standard PDF annotations — highlights, underlines, strike-outs,
boxes, comments and sticky notes — to an existing PDF. Every PDF viewer shows,
edits and prints them, and the user can delete any of them later. The pages and
text are **not changed**. Like `edit_doc` it changes the PDF **in place** unless
you ask for a new document or a temporary copy (step 3).

It is not for changing the PDF's own text (a PDF's pages are fixed — to change
wording, make a new document with `create_pdf`/`create_word` from its text), nor
for cutting or merging pages (`pdf_pages`).

## Step 1 — decide what to mark, using the text

You can only mark text that is really in the PDF, so work from what is there:

- `read_doc` shows the document's text (and, for long ones, the page you ask
  for). Pick the **exact words** from it; do not retype a passage from memory.
- To find which pages hold many phrases, use **one** `check_text` call with
  `texts: [...]` (never one call per phrase).
- Page numbers shown by `read_doc`/`check_text` are the ones `edit_pdf` uses.

A scanned PDF has no text layer, so there is nothing to find: tell the user. You
can still add positioned notes (below).

## Step 2 — one call with all the marks

```json
{"doc": "u.12", "marks": [
  {"kind": "highlight", "text": "total amount due", "page": 2, "comment": "Does not match the order (EUR 1,180)"},
  {"kind": "strikeout", "text": "30 days", "page": 3, "comment": "Should be 14 days per the offer"},
  {"kind": "underline", "text": "late payment interest", "color": "blue"},
  {"kind": "note", "page": 1, "x": 72, "y": 60, "comment": "Whole document needs a legal review"}
]}
```

Call the tool **once** with every mark (up to 200). Several calls would make several rounds of edits (or, with `save_as`, a pile of
near-identical PDFs).

### Phrases

- `text` must match what is printed, ignoring upper/lower case. Copy it from the
  document text.
- It is matched **within one line**. If a sentence wraps, mark its first few
  words (or the part on one line) instead of the whole sentence.
- A **table row is one line**. To mark a whole row ("box the Memory row"),
  give the row's full text as `read_doc` shows it, cells separated by single
  spaces, e.g. `"3 Memory 32 GB DDR5 1 219,00 219,00"`. The mark spans
  everything the phrase matched, so marking only one cell's word marks only
  that cell.
- **Every** occurrence is marked unless you narrow it. "the", "EUR" or a name
  that appears on every page will bury the document, so: add `page`, add `nth`
  (the nth occurrence counting from page 1), or use a longer, more specific
  phrase. A mark stops at 500 matches and says so.
- Shorter is safer than longer: a long phrase that is split differently in the
  PDF is not found; a few distinctive words almost always are.

### Kinds and colours

| kind | use it for | default colour |
|---|---|---|
| `highlight` | "look at this", key figures, definitions | yellow |
| `underline` | emphasis, terms to remember | blue |
| `strikeout` | text that is wrong or should go | red |
| `squiggly` | doubtful or needs checking | red |
| `box` | draws a frame round the phrase | red |
| `note` | a sticky-note icon at the phrase (or at x, y on a page) | yellow |

`color` is `yellow`, `green`, `blue`, `pink`, `red`, `orange`, `purple` or
`#RRGGBB`. When the user marks several *kinds* of finding, give each kind its own
colour and say what the colours mean in your reply (e.g. red = error, green =
confirmed, yellow = to check). Do not invent a colour scheme the user did not
need: with one kind of mark, use the default.

### Comments

`comment` is the text the reader sees when they hover over or click the mark. It
is where your reasoning belongs: say **what** is wrong or noteworthy and, when it
helps, what it should be — short and specific ("Invoice says 1,200; order says
1,180"), not "check this". Any kind can carry a comment. A comment on a long
document is a finding the user must be able to act on without asking you.

### Notes without a phrase

`{"kind": "note", "page": 2, "x": 72, "y": 90, "comment": "…"}` puts a sticky
note at a spot on the page: `x`, `y` are points from the page's **top-left**
corner (an A4 page is about 595 × 842). Use it for a remark about a whole page or
for scanned PDFs. When there is a phrase to attach it to, use `text` instead.

## Step 3 — where does the result go?

There are three places, like `edit_doc`:

1. **Replace the original** — the default: leave `save_as` and `save_to` out. The
   saved PDF keeps its id, title and place and now shows the marks. One step back
   is kept (`edit_doc` with `undo: true`). A sandbox PDF (`tmp://a.pdf`) is
   changed where it is.
2. **A new document** — `save_as: "<title>"`. The original is untouched and the
   user gets a second PDF in their files (in the project in a project session,
   else personal; `scope: "shared"` for owners/admins). A PDF made by
   `create_pdf` is marked in place like any other: its marks are put back when
   its text is later edited with `edit_doc`.
3. **A temporary copy** — `save_to: "tmp://marked.pdf"` (`overwrite: true` to
   replace an earlier one). Write that path in your reply and the user gets a
   **download link**; the file is removed after a while and is not in their
   files. Also use it to chain steps (mark, then mark more) before the final save.

**If the user did not say which, ask before you mark**, with `ask_user`, in one
short question that names the options, e.g. "Should I add the marks to the PDF
itself, save a marked copy as a new document, or give you a temporary copy to
download?" Say what each does in a few words (the original is lost only if they
choose replace, and that can be taken back one step). Do **not** ask when the
request already says: "save as", "new version", "keep the original", "a marked
copy" mean a new document; "in the PDF", "replace", "overwrite" mean in place;
"give me a copy to download" means temporary. Also do not ask for a PDF you made
yourself earlier in this conversation, or again once the user has told you their
preference: use the same answer.

When the work is done and the user wants to keep the file, make sure it is a
saved document: if you used `save_to`, `copy_doc` it (no `target`) to save it.

## Step 4 — report what happened

The result lists each mark: how many annotations it made and on which pages, or
**NOT done** with the reason. Tell the user:

- where the result is: the document's id and title (in place, or the new one), or
  the `tmp://` path for a temporary copy (say it is a download link and is removed
  after a while), and how many annotations it has;
- what each colour means, if you used several;
- every phrase that was **not found** — and what you would try (the PDF may print
  it differently, or break it over lines). Do not claim a mark you did not make.
  If nothing was marked, nothing is changed or saved; look at the text again with `read_doc`
  and retry with phrases copied from it.

If the user asks for a change ("not that one", "make the notes green") after an
in-place edit, call `edit_doc` with `undo: true` first (it takes the last marks
off), then call again with the corrected marks; after a new document or a
temporary copy, make the new call on the **original** document. Do not pile new
marks on the previous result unless they want both.

## Limits to know

- Marking an already marked PDF is fine: the new marks are added to the old ones.
- Annotations are not part of the page: text extraction (and so `search_docs`) of
  the new file reads the same as the original. If the user wants the comments
  themselves in a list, put them in your reply.
- Annotating a **digitally signed** PDF breaks its signature. Say so if the file
  looks like an officially signed document and the user needs it valid.
- Removing existing annotations is not supported; to start clean, annotate the
  original.
