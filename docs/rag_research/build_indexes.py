#!/usr/bin/env python3
"""
Build every index variant into its own SQLite DB under data/indexes/.

Each variant: chunk the corpus, embed the chunks (cached), store vectors +
sentence-span provenance. A summary of chunk statistics is written to
data/index_stats.json for the report.

Run:  python3 build_indexes.py
"""
import json
import os
import sys

import numpy as np

import chunkers
from lib import DATA, Embedder, Sentence, VectorStore, load_corpus, segment_corpus

IDX_DIR = os.path.join(DATA, "indexes")


def variants(sents: list[Sentence], emb: Embedder) -> dict:
    """name -> list of chunk dicts."""
    print("  building chunk sets...", file=sys.stderr)
    # Token-cap sweep (mech_256..mech_1500) isolates the effect of the chunk
    # token budget. 1500 approaches the embedding model's context window
    # (n_ctx = 1536 for granite-embedding-311M), the largest usable cap here.
    v = {
        "mech_256": chunkers.mechanical(sents, 256),
        "mech_512": chunkers.mechanical(sents, 512),
        "mech_1024": chunkers.mechanical(sents, 1024),
        "mech_1500": chunkers.mechanical(sents, 1500),
        "overlap_1024": chunkers.overlapping(sents, 1024),
        "overlap_1500": chunkers.overlapping(sents, 1500),
        "structure_1024": chunkers.structure(sents, 1024),
        "per_sentence": chunkers.per_sentence(sents),
    }
    for gate in (0.05, 0.10, 0.15, 0.20, 0.25):
        name = f"successive_g{gate:.2f}"
        print(f"  successive gate={gate} ...", file=sys.stderr)
        v[name] = chunkers.successive(sents, emb, gate=gate, max_tok=1024)

    # Improved sentence-level semantic chunking (see REPORT §"making successive
    # useful"). Uses the precomputed, L2-normalised per-sentence embeddings, so
    # it is a single batched pass rather than thousands of sequential calls.
    M = emb.embed([s.text for s in sents])
    M = M / (np.linalg.norm(M, axis=1, keepdims=True) + 1e-9)
    for gate in (0.12, 0.15, 0.18):
        v[f"sem_adjacent_g{gate:.2f}"] = chunkers.semantic_adjacent(sents, M, gate=gate)
    for gate in (0.15, 0.18, 0.22):
        v[f"sem_centroid_g{gate:.2f}"] = chunkers.semantic_centroid(sents, M, gate=gate)
    return v


def chunk_stats(chunks: list[dict]) -> dict:
    toks = np.array([c["n_tok"] for c in chunks])
    spans = np.array([c["sent_end"] - c["sent_start"] + 1 for c in chunks])
    return {
        "n_chunks": len(chunks),
        "tok_mean": round(float(toks.mean()), 1),
        "tok_median": int(np.median(toks)),
        "tok_p95": int(np.percentile(toks, 95)),
        "tok_max": int(toks.max()),
        "sent_per_chunk_mean": round(float(spans.mean()), 2),
        "total_tok": int(toks.sum()),
    }


def main():
    os.makedirs(IDX_DIR, exist_ok=True)
    # Budget chunks in the embedding model's own tokens (granite), so caps map
    # directly onto the server's 1500-token limit.
    import lib
    lib.TOK = lib.gtok
    corpus = load_corpus()
    sents = segment_corpus(corpus)
    print(f"corpus: {len(sents)} sentences (granite-token budgeting)", file=sys.stderr)
    emb = Embedder()

    vs = variants(sents, emb)
    stats = {}
    for name, chunks in vs.items():
        st = chunk_stats(chunks)
        stats[name] = st
        print(f"[{name}] {st['n_chunks']} chunks, "
              f"~{st['tok_mean']} tok/chunk", file=sys.stderr)
        # embed all chunk texts (cached/batched)
        vecs = emb.embed([c["text"] for c in chunks])
        store = VectorStore(os.path.join(IDX_DIR, f"{name}.sqlite"))
        store.reset()
        store.set_meta(name=name, **st)
        store.add(chunks, vecs)

    with open(os.path.join(DATA, "index_stats.json"), "w") as f:
        json.dump(stats, f, indent=2)
    print(f"\nembedder: {emb.calls} HTTP calls, {emb.cache_hits} cache hits, "
          f"{emb.tokens} tokens", file=sys.stderr)
    print(json.dumps(stats, indent=2))


if __name__ == "__main__":
    main()
