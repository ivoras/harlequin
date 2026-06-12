# Harlequin — talk outline (CroAI)

**Title:** Harlequin
**Subtitle:** A local-first client-server AI agent harness
**Audience:** ML experts; technical/engineering talk, numbers and charts welcome.
**Deck (source of truth):** `slides-pptx.pptx` (PowerPoint 2007+). Edit it
directly — see `AGENTS.md` in this directory for how. This outline tracks the
deck; when they disagree, the PPTX wins.

> Note: an earlier Quarto/reveal.js plan (`slides.qmd` → `slides.html`) was
> dropped. The PowerPoint file is the live deliverable now.

## Narrative arc

"Agent harnesses are all single-user laptop tools talking to cloud APIs.
What breaks — and what gets interesting — when the harness is a multi-user
server and the model is a local one on a single GPU?" Every section returns to
one of two tensions: **the GPU is a contended resource** and **every token
costs latency**.

## Slide map (current PPTX — 30 slides, 0-indexed)

| # | Title / content | Section |
|---|-----------------|---------|
| 0  | Harlequin (title) | Hook |
| 1  | Why? | Thesis |
| 2  | What? | Thesis |
| 3  | *(full-slide demo GIF — watchprice.gif)* | Demo |
| 4  | Agent loop (per-user) — architecture diagram | Architecture |
| 5  | Local-model-optimised | Local model |
| 6  | LLM performance: mostly RAM bandwidth | Local model |
| 7  | But… UX hugely depends on PP (compute-bound) | Local model |
| 8  | Example performance: Strix Halo | Local model |
| 9  | Dynamic per-request deadlines from rolling PP-rate; live PP progress | Local model |
| 10 | One GPU, many jobs | Background work |
| 11 | Gating keys off the `local` provider; preempted by live turns | Background work |
| 12 | KV-cache economics | KV cache |
| 13 | Reasoning-token stripping diverges the prefix; SWA/checkpoints | KV cache |
| 14 | Client-server | Architecture |
| 15 | Single-executable deployment | Deployment |
| 16 | SQLite everywhere — three-tier layout, per-request DBs, FTS5+vec | SQLite |
| 17 | **Sandboxed paths** — `skill://` / `storage://` / `tmp://` | Sandbox |
| 18 | Embeddings to speed things up — hybrid FTS+vector RRF | Embeddings |
| 19 | Measured: slot-key embedding leg (memslot eval) | Embeddings |
| 20 | *(memslot chart — memslot.png)* | Embeddings |
| 21 | The memory model | Memory |
| 22 | Skill files are templated Markdown | Skills |
| 23 | Hats | Hats |
| 24 | Notifications & polling | Notifications |
| 25 | No bash. JavaScript! | Sandbox/JS |
| 26 | Same sandbox everywhere (run_js · skill templating · tools · cron) | Sandbox/JS |
| 27 | Making a 26B model behave | Prompt engineering |
| 28 | Numbers recap | Recap |
| 29 | Takeaways | Recap |

Slide 17 (**Sandboxed paths**) enumerates the three URI schemes the goja
sandbox and tools resolve, per-user and path-traversal-guarded:
`skill://<skill>/<path>` (read-only skill-bundle files, hat→user→org→deployed
overrides), `storage://<path>` (persistent `.storage`), `tmp://<path>`
(transient `.tmp`).

## Key numbers (June 2026 run — verify before presenting)

- PP ~225–350 tok/s, TG ~24–28 tok/s; 21k-token cold prompt ≈ 95 s.
- memslot eval (1,000 memories / 154 queries): MRR 0.849→0.858 with the
  slot-key leg; key+value leg *hurts*, R@1 0.799→0.643 at w=2.
- Mid-turn KV reuse 150/21k tokens vs full 21k reprocess at turn boundaries;
  checkpoints ~106 MiB in host RAM (shared `--cache-ram`) vs `--swa-full` (VRAM).

## Assets in this directory

- `slides-pptx.pptx` — the deck (source of truth).
- `arch.png` — architecture diagram (slide 4).
- `memslot.png` — embeddings eval chart (slide 20).
- `watchprice.gif` / `watchprice_deres.gif` — demo GIF (slide 3; deres = smaller).
- `AGENTS.md` — how to edit the deck programmatically.

## TODO before the talk

- [ ] 2–3 transcript snippets for "Making a 26B model behave" (recited vs computed π).
- [ ] Re-run `eval/memslot` if the corpus/embedder changes; deck numbers are June 2026.
- [ ] Optional PDF fallback: export from PowerPoint/LibreOffice (File → Export → PDF).
