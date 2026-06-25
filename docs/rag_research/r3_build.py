#!/usr/bin/env python3
"""
Build single-embedder indexes for paper3 into data/r3idx/{config}/{variant}.sqlite.

Every config runs ONE model for the whole path: the per-sentence embeddings that
drive semantic boundaries, the chunk vectors, and (at eval time) the query
vectors all come from the model named by `config`. Variants built:

  per_sentence, mech_256/512/1024/1500, overlap_512/1024  (size sweep)
  semadj_g<gate>   adjacent-sentence semantic chunking, gate swept per model
                   at percentiles of THIS model's adjacent-drift distribution
                   (the drift scale is model-specific, so a fixed gate set would
                   degenerate for some models).

Usage:  python3 r3_build.py <config>      e.g.  granite | snowflake_np | gemma
"""
import json
import os
import sys

import numpy as np

import chunkers
import lib
from lib import DATA, EMBEDDERS, SERVER_OF, Embedder, VectorStore, load_corpus, segment_corpus

R3IDX = os.path.join(DATA, "r3idx")

# Adjacent-drift percentiles at which to place the semantic gate sweep. Spanning
# p20..p85 guarantees the gate bites (produces a real range of chunk sizes) on
# every model regardless of its absolute drift scale.
GATE_PCTILES = (20, 30, 40, 50, 60, 70, 85)

SIZE_VARIANTS = ("per_sentence", "mech_256", "mech_512", "mech_1024", "mech_1500",
                 "overlap_512", "overlap_1024")


def idx_path(config: str, variant: str) -> str:
    return os.path.join(R3IDX, config, f"{variant}.sqlite")


def gate_grid(M: np.ndarray) -> list[float]:
    """Per-model gate sweep from the adjacent-sentence drift distribution."""
    drift = 1.0 - np.sum(M[:-1] * M[1:], axis=1)        # 1-cos of neighbours
    gates = sorted({round(float(np.percentile(drift, p)), 4) for p in GATE_PCTILES})
    return gates


def variant_sets(sents, M, gates):
    v = {
        "per_sentence": chunkers.per_sentence(sents),
        "mech_256": chunkers.mechanical(sents, 256),
        "mech_512": chunkers.mechanical(sents, 512),
        "mech_1024": chunkers.mechanical(sents, 1024),
        "mech_1500": chunkers.mechanical(sents, 1500),
        "overlap_512": chunkers.overlapping(sents, 512),
        "overlap_1024": chunkers.overlapping(sents, 1024),
    }
    for g in gates:
        v[f"semadj_g{g:.4f}"] = chunkers.semantic_adjacent(sents, M, gate=g)
    return v


def build(config: str):
    lib.GTOK_MODEL = SERVER_OF[config]          # namespace token cache by model
    lib.TOK = lib.gtok                          # budget in this model's tokens
    sents = segment_corpus(load_corpus())
    emb = Embedder(config, verbose=True)
    M = emb.embed([s.text for s in sents], role="doc")
    M = M / (np.linalg.norm(M, axis=1, keepdims=True) + 1e-9)
    gates = gate_grid(M)
    sets = variant_sets(sents, M, gates)
    outdir = os.path.join(R3IDX, config)
    os.makedirs(outdir, exist_ok=True)
    manifest = {"config": config, "model": EMBEDDERS[config]["model"],
                "dim": emb.dim, "n_sent": len(sents), "gates": gates, "variants": {}}
    for name, chunks in sets.items():
        vecs = emb.embed([c["text"] for c in chunks], role="doc")
        store = VectorStore(idx_path(config, name), dim=emb.dim)
        store.reset()
        tok_mean = float(np.mean([c["n_tok"] for c in chunks]))
        meta = dict(name=name, config=config, n_chunks=len(chunks), tok_mean=tok_mean,
                    sent_per_chunk=float(np.mean([c["sent_end"]-c["sent_start"]+1 for c in chunks])))
        if name.startswith("semadj_g"):
            meta["gate"] = float(name.split("_g")[1])
        store.set_meta(**meta)
        store.add(chunks, vecs)
        manifest["variants"][name] = {"n_chunks": len(chunks), "tok_mean": tok_mean,
                                      **({"gate": meta["gate"]} if "gate" in meta else {})}
        print(f"[{config}/{name}] {len(chunks)} chunks ~{tok_mean:.0f} tok", file=sys.stderr)
    json.dump(manifest, open(os.path.join(outdir, "manifest.json"), "w"), indent=1)
    print(f"done {config}: {emb.calls} embed calls, {emb.cache_hits} cached", file=sys.stderr)


if __name__ == "__main__":
    build(sys.argv[1] if len(sys.argv) > 1 else "granite")
