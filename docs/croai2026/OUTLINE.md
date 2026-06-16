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

## Slide map (current PPTX — 37 slides, 0-indexed)

| # | Title / content | Section |
|---|-----------------|---------|
| 0  | Harlequin (title) | Hook |
| 1  | Why? | Thesis |
| 2  | I'll make my own theme park! | Hook |
| 3  | What? | Thesis |
| 4  | *(full-slide demo GIF — watchprice.gif)* | Demo |
| 5  | The agentic loop (one turn) — flowchart | Architecture |
| 6  | Agent loop (per-user) — architecture diagram | Architecture |
| 7  | Local-model-optimised | Local model |
| 8  | LLM performance: mostly RAM bandwidth | Local model |
| 9  | But… UX hugely depends on PP (compute-bound) | Local model |
| 10 | Example performance: Strix Halo | Local model |
| 11 | Dynamic per-request deadlines from rolling PP-rate; live PP progress | Local model |
| 12 | One GPU, many jobs | Background work |
| 13 | Gating keys off the `local` provider; preempted by live turns | Background work |
| 14 | KV-cache economics | KV cache |
| 15 | Reasoning-token stripping diverges the prefix; SWA/checkpoints | KV cache |
| 16 | Client-server | Architecture |
| 17 | Single-executable deployment | Deployment |
| 18 | SQLite everywhere — three-tier layout, per-request DBs, FTS5+vec | SQLite |
| 19 | Sandboxed paths — `skill://` / `storage://` / `tmp://` | Sandbox |
| 20 | Embeddings to speed things up — hybrid FTS+vector RRF | Embeddings |
| 21 | Measured: slot-key embedding leg (memslot eval) | Embeddings |
| 22 | *(memslot chart — memslot.png)* | Embeddings |
| 23 | The memory model | Memory |
| 24 | Skill files are templated Markdown | Skills |
| 25 | Hats | Hats |
| 26 | Notifications & polling | Notifications |
| 27 | No bash. JavaScript! | Sandbox/JS |
| 28 | Same sandbox everywhere (run_js · skill templating · tools · cron) | Sandbox/JS |
| 29 | Making a 26B model behave | Prompt engineering |
| 30 | *(image)* | — |
| 31 | *(image)* | — |
| 32 | *(image)* | — |
| 33 | Impact of quantizations — Qwen3.6 & Gemma 4 MoE | Local model |
| 34 | DiffusionGemma — diffusion decoding, speed ↔ quality | Local model |
| 35 | Numbers recap | Recap |
| 36 | Takeaways | Recap |

Slide 5 (**The agentic loop**) is a flowchart of one turn: user message →
assemble context → stream LLM call (PP → TG) → *tool calls?* → yes: dispatch
sandboxed tools, append results, loop back (≤ max_steps); no: stream final
answer, then background memory extraction.

Slide 19 (**Sandboxed paths**) enumerates the three URI schemes the goja
sandbox and tools resolve, per-user and path-traversal-guarded:
`skill://<skill>/<path>` (read-only skill-bundle files, hat→user→org→deployed
overrides), `storage://<path>` (persistent `.storage`), `tmp://<path>`
(transient `.tmp`).

Slide 33 (**Impact of quantizations**): Q8 ≈ lossless, Q4_K the sweet spot, ≤Q3
degrades (loss in the distribution tails). Qwen3.6-35B-A3B uses post-training
quant (Q4_K_M ≈ BF16, mean KLD ~0.01); Gemma 4 26B-A4B uses QAT, so Q4 ≈ BF16
*if* the GGUF keeps QAT's BF16 scales (naive Q4_0 70.2% top-1 vs Unsloth
UD-Q4_K_XL 85.6%). MoE twist: expert FFNs tolerate low bits; attention/router/
shared layers and a few critical experts need mixed-precision.

Slide 34 (**DiffusionGemma**): same Gemma 4 26B-A4B MoE base as our chat model,
decoded by discrete text diffusion — denoises a 256-token canvas in parallel
(≤48 steps), ~1,100+ tok/s/user (H100 FP8), ~4–6× the AR baseline. Quality
trails standard Gemma 4 on every benchmark (MMLU-Pro 77.6 vs 82.6, AIME 69.1 vs
88.3), widening on hard reasoning/vision. Fits latency-bound structured subtasks
(code infill, classification, titling); keep the AR model for reasoning.

## Key numbers (June 2026 — verify before presenting)

- PP ~225–350 tok/s, TG ~24–28 tok/s; 21k-token cold prompt ≈ 95 s.
- memslot eval (1,000 memories / 154 queries): MRR 0.849→0.858 with the
  slot-key leg; key+value leg *hurts*, R@1 0.799→0.643 at w=2.
- Mid-turn KV reuse 150/21k tokens vs full 21k reprocess at turn boundaries;
  checkpoints ~106 MiB in host RAM (shared `--cache-ram`) vs `--swa-full` (VRAM).
- Quant (slide 33) & DiffusionGemma (slide 34) numbers — see notes above.

## Assets in this directory

- `slides-pptx.pptx` — the deck (source of truth).
- `arch.png` — architecture diagram (slide 6, "Agent loop (per-user)").
- `memslot.png` — embeddings eval chart (slide 22).
- `watchprice.gif` / `watchprice_deres.gif` — demo GIF (slide 4; deres = smaller).
- `AGENTS.md` — how to edit the deck programmatically.
- Slides 30–32 are images added directly in PowerPoint (content not catalogued here).

## Research sources (slides 33–34)

- [Unsloth — Gemma 4 QAT](https://unsloth.ai/docs/models/gemma-4/qat) ·
  [Unsloth — Qwen3.6](https://unsloth.ai/docs/models/qwen3.6) ·
  [Qwen3 MoE quant roundup](https://gist.github.com/ubergarm/0f9663fd56fc181a00ec9f634635eb38)
- [DiffusionGemma model card](https://ai.google.dev/gemma/docs/diffusiongemma/model_card) ·
  [vLLM — DiffusionGemma](https://vllm.ai/blog/2026-06-10-diffusion-gemma) ·
  [Google — DiffusionGemma](https://blog.google/innovation-and-ai/technology/developers-tools/diffusion-gemma-faster-text-generation/)

## TODO before the talk

- [ ] 2–3 transcript snippets for "Making a 26B model behave" (recited vs computed π).
- [ ] Re-run `eval/memslot` if the corpus/embedder changes; deck numbers are June 2026.
- [ ] Optional PDF fallback: export from PowerPoint/LibreOffice (File → Export → PDF).
