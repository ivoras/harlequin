# Embedding model comparison (local vs hosted)

*2026-07-12 — measuring whether hosted embedding APIs would improve Harlequin's
RAG retrieval over the current local model, and by how much.*

## Setup

- **Eval set**: the `docs/rag_research` study corpus — the consolidated TEU+TFEU
  treaty (CELEX:12016M/TXT), segmented by `lib.segment_corpus` into **2112
  sentences**, with **150 questions** sampled (seed 42) from
  `data/eval_questions.json`, each with gold support-sentence ids
  (`data/answer_sets.json`).
- **Task**: sentence retrieval by plain cosine over L2-normalised vectors —
  the dense arm in isolation. No FTS5/RRF fusion, no chunking; this isolates
  embedding quality from the rest of the pipeline.
- **Prefixes**: each model was queried with its production convention — the
  local Qwen with the server's `query_prefix: "query: "`, the hosted Qwen 4B
  with its documented `Instruct:/Query:` prompt (hosted endpoints don't apply
  it server-side), granite/Gemini/Mistral/OpenAI with raw text both sides.
- **Serving**: local models via llama.cpp `/v1/embeddings`; hosted models via
  OpenRouter. Granite vectors were read from the rag_research embedding cache
  (`data/embed_cache.sqlite`) rather than re-served.
- **Harness**: `docs/rag_research/embed_eval_hosted.py` (reusable — one `MODELS`
  entry per candidate, vectors cached per model as `.npy`).

## Results

| model | dim | hit@1 | hit@5 | hit@10 | MRR@10 |
|---|---:|---:|---:|---:|---:|
| **qwen/qwen3-embedding-4b** (OpenRouter) | 2560 | **0.627** | **0.800** | **0.820** | **0.698** |
| openai/text-embedding-3-large (OpenRouter) | 3072 | 0.607 | 0.767 | 0.813 | 0.679 |
| mistralai/mistral-embed-2312 (OpenRouter) | 1024 | 0.593 | 0.760 | 0.793 | 0.668 |
| google/gemini-embedding-001 (OpenRouter) | 3072 | 0.580 | 0.780 | 0.807 | 0.666 |
| Qwen3-Embedding-0.6B (local, **current**) | 1024 | 0.507 | 0.693 | 0.760 | 0.588 |
| granite-embedding-311M (local) | 768 | 0.493 | 0.687 | 0.727 | 0.571 |

## Interpretation

- **Every hosted model clearly beats the local 0.6B**: +0.08–0.11 MRR@10,
  +7–12pp hit@1. The gap is consistent across all cutoffs — real, not noise.
- **Within the hosted tier the differences are mostly noise.** With n=150 the
  standard error is ~4pp, so qwen3-4b's lead over gemini/mistral is suggestive
  rather than conclusive. Treat the four as one tier, qwen3-4b the best point
  estimate. It is the same family as the current model, scaled 0.6B → 4B.
- **Granite lands slightly below the 0.6B Qwen**, consistent with the original
  rag_research single-embedder study that picked Qwen for local serving.
- **Production impact will be smaller than the dense-arm numbers**: Harlequin
  fuses dense + FTS5 via weighted RRF, and the lexical arm rescues many dense
  misses. This eval is also one domain (English legal text) at sentence
  granularity.

## Operational notes for switching

- Vectors from different models don't mix: any switch requires re-embedding
  every corpus and memory (recreate `doc_chunks_vec` / `memories_vec` /
  `memory_slots_vec` contents), regardless of dimension.
- **mistral-embed-2312** is the operationally cheapest switch: same 1024 dim
  as the existing `vec0` tables (no schema change), top-tier quality.
- **qwen3-embedding-4b** needs `dim: 2560` and recreated vector tables. It is
  already configured as the embed slot of the (unused) `openrouter` model
  suite in `server.yaml`.
- Hosted embeddings put every ingest, memory write, and search on a paid
  network path, add its latency, and break RAG entirely when offline. The
  current all-local path has none of those costs — the quality gap buys
  independence.

## Reproducing / extending

The harness embeds the corpus + sampled questions per model, caches vectors,
and prints the table. To add a candidate: one entry in `MODELS` (URL, model
id, prefixes, API key env) and re-run — cached models are not re-fetched.
A quick single-pair smoke test of any OpenAI-compatible embeddings endpoint:

```
EMBED_API_KEY=$OPENROUTER_API_KEY go run ./cmd/embedsim \
  -url https://openrouter.ai/api/v1 -model google/gemini-embedding-001 \
  "text a" "text b"
```
