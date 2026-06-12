# Harlequin — talk outline (CroAI)

**Title:** Harlequin
**Subtitle:** A local-first client-server AI agent harness
**Audience:** ML experts; technical/engineering talk, numbers and charts welcome.
**Deck:** `slides.qmd` (Quarto → reveal.js, dark theme; speaker notes in `::: notes` blocks).
**Output:** `slides.html` — a single self-contained file (GIF, Mermaid, CSS all
inlined): open it directly in any browser from `file://`, no server, no network.

## Narrative arc

"Agent harnesses are all single-user laptop tools talking to cloud APIs.
What breaks — and what gets interesting — when the harness is a multi-user
server and the model is a local one on a single GPU?" Every section returns to
one of two tensions: **the GPU is a contended resource** and **every token
costs latency**.

## Outline (≈30–35 min + Q&A)

1. **Title / hook** (1 min)
2. **Thesis & architecture overview** (3 min) — client-server agent loop,
   Go + SQLite + goja stack, llama.cpp chat (26B A4B MoE) + embedder (311M).
3. **Local-model-optimised** (6 min, 3 slides)
   - Measured PP ~225–350 tok/s, TG ~24–28 tok/s; 21k-token cold prompt ≈ 95 s.
   - Dynamic per-request deadlines from rolling PP-rate; streamed PP progress.
   - *Suggested extra topic A — "One GPU, many jobs"*: background LLM work
     (extraction, judges, titler, sweeps) gated behind a single slot and
     **preempted by live turns in <1 s**; gating keys off the `local` provider
     name. War story: three ungated paths found by tailing llama.cpp slot logs.
   - *Suggested extra topic B — "KV-cache economics"*: mid-turn reuse 150/21k
     tokens vs full 21k reprocess at turn boundaries; thinking-token stripping
     diverges the prefix; SWA all-or-nothing rewind; checkpoints (~106 MiB,
     host RAM, shared `--cache-ram` budget) vs `--swa-full` (VRAM) trade-offs;
     harness rule: byte-stable prompt prefixes.
4. **Client-server** (3 min) — roles, interfaces (TUI/Web/Cron/Telegram),
   SSE streaming, queued messages, `ask_user` turn semantics.
5. **Single-executable deployment** (2 min) — embed.FS for skills/hats/
   migrations/web UI; vendored SQLite headers; hash-manifest asset deploys.
6. **SQLite everywhere** (3 min) — three-tier layout, per-user DB opened per
   request, composite cross-file ids, FTS5 + sqlite-vec next to the data.
   Prepare for the "why not Postgres?" question (answer: ops + isolation;
   admit per-file write concurrency limits).
7. **Embeddings to speed up operations** (4 min, has chart) — hybrid
   FTS+vector RRF; embeddings pre-filter candidates so the big model judges
   rarely; **memslot eval results** (1,000 memories / 154 queries: MRR
   0.849→0.858 with the slot-key leg; key+value leg *hurts*, R@1 0.799→0.643
   at w=2). The "embedding the value is noise for attribute lookups" insight
   lands well with this audience.
8. **The memory model** (4 min) — user/shared scopes, slot keys,
   auto-extraction (background, preemptible), conflict judge with the
   **confidence ≥ 7** gate, TTL/pinning/provenance, cross-scope reconcile.
9. **Hats** (2 min) — persona = prompt + skill set per conversation; keeps the
   default prompt small (PP cost again).
10. **Notifications / polling** (3 min) — server-side store, 1-min client
    polling + presence, in-app/email/Telegram channels; cron `kind:js`
    (LLM-free checks) vs `kind:skill` (full turn) cost split.
11. **No bash, JS!** (3 min) — goja sandbox; pro: security (no fs/exec/net
    except allow-listed fetch, interrupt timeouts, output caps — viable on a
    multi-user server); con: flexibility (ES5.1+ subset, no npm). Anecdote:
    model computes 200 digits of π with a BigInt Machin series in the sandbox,
    self-corrects a sign bug — algorithm-not-recall enforced by prompt.
12. *Suggested extra topic C — "Making a 26B model behave"* (3 min) — prompt
    engineering for small models (mechanical decision rules, examples over
    abstractions), computed-answers policy, structured judges, grounding
    rules. Harness design substitutes for model capability.
13. **Numbers recap + takeaways** (2 min) — one-table summary, five takeaways.

## Suggested extra topics (beyond the requested list)

- **A. One GPU, many jobs** — background-work scheduling/preemption (slide in deck).
- **B. KV-cache economics on local models** — prefix stability, SWA, checkpoints (slide in deck).
- **C. Making a small model behave** — prompt + judge engineering (slide in deck).
- (bench) **Session-log-driven debugging** — JSONL trajectories + llama.cpp slot
  logs as the observability story; could fold into A/B if time is short.
- (bench) **Skills as markdown+JS** — `<?js ?>` templating, frontmatter tools,
  override precedence (user → org → baked); currently folded into Hats/deploy slides.

## TODO before the talk

- [x] Architecture diagram (Mermaid, slide 4); before/after slot-timeline figure
      for topic A still optional (could be a Mermaid gantt).
- [x] Demo GIF — full-slide `public/watchprice.gif` after the Thesis slide.
- [ ] 2–3 transcript snippets for topic C (recited π vs computed π).
- [ ] Re-run `eval/memslot` if the corpus/embedder changes; numbers in the deck
      are from the June 2026 run.
- [ ] Optional PDF fallback: open `slides.html`, press `E` (print view), then
      print-to-PDF from the browser. (The HTML itself already works offline.)

## Presenting

```sh
quarto render slides.qmd     # rebuild slides.html (quarto is in ~/.local/bin)
quarto preview slides.qmd    # edit with live reload
```

Then just open `slides.html` in a browser — it is fully self-contained
(file://, no server, no network). Speaker view with notes: press `S`.
Overview: `O`. Print view: `E`.
