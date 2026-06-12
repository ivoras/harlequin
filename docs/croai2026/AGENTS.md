# Working with the slide deck

- **Source of truth:** `slides-pptx.pptx` (PowerPoint 2007+). `OUTLINE.md` tracks
  it but the PPTX wins on any disagreement. Keep `OUTLINE.md`'s slide map in sync
  after structural edits.
- **Editing:** use `python-pptx`. The venv is at the repo git root: `.venv/`
  (gitignored). Run `<repo-root>/.venv/bin/python`. Install if missing:
  `python3 -m venv .venv && .venv/bin/pip install python-pptx`.
- **Slide order ≠ file numbers.** `ppt/slides/slideN.xml` names are not the
  presentation order; iterate `Presentation(...).slides` (it is in order) or
  reorder via the `sldIdLst`. To insert after slide *i*: `add_slide()` (appends),
  then move its `sldId` to index *i+1* in `prs.slides._sldIdLst`.
- **Match style:** clone the layout of the neighbouring slide (`Title and
  Content`), and use the `Courier` font for code/URIs (see the SQLite/Sandboxed
  slides). Set the title via the placeholder with `idx == 0`, body via `idx == 1`.
- **Safety:** close the file in PowerPoint/LibreOffice before writing, and back
  it up first (`cp slides-pptx.pptx /tmp/`). Verify after saving by reopening and
  printing the slide titles.
- **Assets:** `arch.png` (slide 4), `memslot.png` (slide 20), `watchprice*.gif`
  (slide 3). `*_deres.gif` is the size-reduced variant.
- **PDF export:** open in PowerPoint/LibreOffice → File → Export → PDF.
