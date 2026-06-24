#!/usr/bin/env python3
"""
Build rework indexes into data/idx/{backend}/{variant}.sqlite.

Chunk *boundaries* are a property of the chunker and are computed once (semantic
methods use granite per-sentence embeddings); the same chunk sets are then
embedded with whichever backend, so the embedding-model ablation changes only
the embedding, not the boundaries.

Usage:
  python3 r2_build.py core granite
  python3 r2_build.py core qwen4b
  python3 r2_build.py confound granite   # size-swept families + null baselines
"""
import os
import sys

import numpy as np

import chunkers
from lib import DATA, Embedder, VectorStore, load_corpus, segment_corpus
import lib
from r2_retrieval import idx_path


# Semantic-boundary gate calibration is per embedder: the adjacent-sentence drift
# (1-cos of neighbouring sentence vectors) lives on a model-specific scale. granite
# sits low (mean ~0.13), Qwen3-Embedding much higher (0.6B: mean ~0.54), so a gate
# tuned for one degenerates the other (too-low gate -> every sentence cut). Each
# entry: the gate used for the single core sem_adjacent variant, the gate sweep,
# and the centroid-variant gate sweep.
GATES = {
    "granite": dict(core=0.12,
                    semadj=(0.08, 0.10, 0.12, 0.14, 0.16, 0.18, 0.20),
                    semcen=(0.12, 0.15, 0.18, 0.22)),
    "qwen4b":  dict(core=0.40,
                    semadj=(0.30, 0.35, 0.40, 0.45, 0.50, 0.55, 0.60),
                    semcen=(0.35, 0.45, 0.55, 0.65)),
    # qwen0.6b adjacent drift: median 0.52, p75 0.70, p90 0.79 on this corpus.
    "qwen06b": dict(core=0.60,
                    semadj=(0.45, 0.50, 0.55, 0.60, 0.65, 0.70, 0.80),
                    semcen=(0.50, 0.60, 0.70, 0.80)),
}


def sentence_matrix(sents, backend: str):
    """L2-normalised per-sentence embeddings for semantic boundaries, computed
    with the SAME embedder used for the chunks — one model the whole path."""
    e = Embedder(backend, verbose=False)
    M = e.embed([s.text for s in sents], role="doc")
    return M / (np.linalg.norm(M, axis=1, keepdims=True) + 1e-9)


def core_sets(sents, M, gate: float):
    return {
        "per_sentence": chunkers.per_sentence(sents),
        "sem_adjacent_g0.12": chunkers.semantic_adjacent(sents, M, gate=gate),
        "structure_1024": chunkers.structure(sents, 1024),
        "mech_256": chunkers.mechanical(sents, 256),
        "mech_512": chunkers.mechanical(sents, 512),
        "mech_1024": chunkers.mechanical(sents, 1024),
        "mech_1500": chunkers.mechanical(sents, 1500),
        "overlap_1024": chunkers.overlapping(sents, 1024),
    }


def confound_sets(sents, M, semadj_gates, semcen_gates):
    v = {}
    for n in (1, 2, 3, 5, 8, 13, 21):
        v[f"fixed_n{n:02d}"] = chunkers.fixed_nsent(sents, n)
    N = len(sents)
    for n in (2, 3, 5, 8, 13):
        nch = max(1, round(N / n))
        for seed in (0, 1, 2):
            v[f"random_n{n:02d}_s{seed}"] = chunkers.random_cuts(sents, nch, seed=seed)
    for g in semadj_gates:
        v[f"semadj_g{g:.2f}"] = chunkers.semantic_adjacent(sents, M, gate=g)
    for g in semcen_gates:
        v[f"semcen_g{g:.2f}"] = chunkers.semantic_centroid(sents, M, gate=g)
    return v


def build(group: str, backend: str):
    lib.TOK = lib.gtok                      # budget in model tokens
    sents = segment_corpus(load_corpus())
    M = sentence_matrix(sents, backend)
    gc = GATES.get(backend, GATES["granite"])
    sets = (core_sets(sents, M, gc["core"]) if group == "core"
            else confound_sets(sents, M, gc["semadj"], gc["semcen"]))
    emb = Embedder(backend, verbose=True)
    outdir = os.path.join(DATA, "idx", backend)
    os.makedirs(outdir, exist_ok=True)
    for name, chunks in sets.items():
        vecs = emb.embed([c["text"] for c in chunks], role="doc")
        store = VectorStore(idx_path(backend, name), dim=emb.dim)
        store.reset()
        store.set_meta(name=name, backend=backend, n_chunks=len(chunks),
                       tok_mean=float(np.mean([c["n_tok"] for c in chunks])),
                       sent_per_chunk=float(np.mean([c["sent_end"]-c["sent_start"]+1 for c in chunks])))
        store.add(chunks, vecs)
        print(f"[{backend}/{name}] {len(chunks)} chunks "
              f"~{np.mean([c['n_tok'] for c in chunks]):.0f} tok", file=sys.stderr)
    print(f"done: {group}/{backend}  ({emb.calls} embed calls, {emb.cache_hits} cached)",
          file=sys.stderr)


if __name__ == "__main__":
    group = sys.argv[1] if len(sys.argv) > 1 else "core"
    backend = sys.argv[2] if len(sys.argv) > 2 else "granite"
    build(group, backend)
