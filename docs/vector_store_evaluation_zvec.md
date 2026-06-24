# Vector store evaluation: zvec vs. sqlite-vec

**Date:** 2026-06-24
**Question:** Should harlequin replace its sqlite-vec vector store with
[alibaba/zvec](https://github.com/alibaba/zvec)?
**Verdict:** No. Stay on sqlite-vec.

## What zvec is

An in-process C++ vector database (Apache 2.0, ~12.4k stars on the core repo).

- Metrics: cosine, L2, inner product, MIPS-L2.
- Indexes: HNSW plus an on-disk DiskANN index (v0.5.0) for billion-scale data.
- WAL persistence; directory-per-collection storage.
- Native full-text search and a hybrid `MultiQuery` that fuses vector + FTS +
  structured filters in one call.
- Headline claim: "searches billions of vectors in milliseconds."
- Concurrency: multiple processes may read a collection; writes are
  single-process exclusive.
- Go support via [`zvec-ai/zvec-go`](https://github.com/zvec-ai/zvec-go) — a
  **CGO** wrapper of the C-API. As of this evaluation that binding repo is
  **experimental, ~10 stars, v0.5**, first released the same month. Needs a C
  compiler and either prebuilt libs (vendor mode) or a CMake/Ninja build.

## Why it does not fit harlequin

1. **Two-store consistency problem (decisive).** zvec is a *separate* vector
   store, so adopting it would not remove SQLite — the relational rows
   (`memories`, `memory_slots`, conflicts, cron, userconfig) and FTS still live
   in SQLite. We would run **both** native engines, and every memory write would
   have to update SQLite *and* zvec across two transaction boundaries, with us
   owning partial-failure reconciliation. Today that is free: vectors are just
   another table in the same ACID file (e.g. `insertSlot` writes `memory_slots`
   and `memory_slots_vec` in one transaction; search joins `vec0` hits back to
   the `memories` table).

2. **Scale mismatch.** A tenant holds on the order of 10–10³ memories/doc-chunks.
   sqlite-vec brute-forces that in well under a millisecond. zvec's strengths
   (DiskANN, billions of vectors) solve a problem we do not have.

3. **More native dependencies, not fewer.** CGO is already a friction point
   (it is why the agent/memory packages have no `_test.go`). zvec keeps CGO and
   adds a second native lib on top of SQLite. Strictly worse on build/deps.

4. **Maturity.** The Go binding is experimental and brand new; betting the store
   on it carries risk for no scale benefit. SQLite underneath sqlite-vec is
   rock-solid.

## The one point in zvec's favor

Its first-class hybrid query (vector + FTS + filters fused natively) is cleaner
than our hand-rolled Reciprocal Rank Fusion over separate FTS5 + `vec0` legs.
But the RRF code already works and is small, so this alone does not justify a
migration plus a second native dependency.

## When this would flip

If a single tenant grew to millions+ of vectors where brute-force sqlite-vec
became too slow or memory-heavy, DiskANN's on-disk index would start to matter.
That is not this workload (a personal/org memory assistant) and is not on a
realistic trajectory.

## Higher-leverage alternatives

Tuning beats replacing the store: the embedding-model distance recalibration
(`memory.search_max_distance`) and the optional Qwen3 query-instruction prefix
are the better investments in retrieval quality.
