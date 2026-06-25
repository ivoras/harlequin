#!/usr/bin/env python3
"""
Exact-extraction probe sweep + FTS5 score-gating experiment.

On two query sets — the exact-extraction probe (data/probe_exact.json) and the
clean (non-misspelled) paraphrase questions — compare on
snowflake/semadj_g0.4341:
  * dense only
  * dense+FTS5 RRF at several FTS5-arm weights
  * dense+FTS5 RRF where weak FTS5 hits (outside the top decile per query) are
    dropped before fusion (the "score-gated hybrid")

The gate keeps only confident lexical matches (rare exact tokens earn high BM25),
so it should add lexical signal on exact queries without polluting paraphrase
ones. Needs the snowflake server on :2235 (probe queries are new). Writes
data/r3_probe_sweep.json.
"""
import json
import os

from lib import DATA, Embedder
from r3_eval import EVALS, Retriever, _first_hit_rank, embed_queries
from r3_rrf_sweep import EVALCONFIG, VARIANT, wrrf

RP = os.environ.get("RPREFIX", "r3")
WEIGHTS = [0.25, 1.0, 2.0, 4.0]


def score_set(qs, accs, qvecs, r, rank_fn):
    r1 = r5 = 0
    for q in qs:
        ranked = rank_fn(q, qvecs[q["id"]])
        rank = _first_hit_rank(ranked, r.by_id, accs[q["id"]])
        r1 += rank == 1
        r5 += bool(rank and rank <= 5)
    n = len(qs)
    return {"n": n, "recall@1": r1 / n, "recall@5": r5 / n}


def gate_top(fts, pct):
    """Keep the strongest hits — the top (100-pct)% of a best-first list. This is
    the same per-query gate Harlequin's documents.Search applies (portable, no
    corpus-specific score constant)."""
    if pct <= 0 or not fts:
        return fts
    import math
    keep = max(1, math.ceil(len(fts) * (100 - pct) / 100.0))
    return fts[:keep]


def run_set(name, qs, accs, qvecs, r):
    out = {}
    out["dense"] = score_set(qs, accs, qvecs, r,
                             lambda q, v: r.dense(v))
    for w in WEIGHTS:
        out[f"hybrid_w{w}"] = score_set(qs, accs, qvecs, r,
                                        lambda q, v, w=w: wrrf(r.dense(v), r.fts5(q["q"]), w))
    for pct in (0, 50, 75, 90):
        out[f"gated_p{pct}"] = score_set(
            qs, accs, qvecs, r,
            lambda q, v, pct=pct: wrrf(r.dense(v), gate_top(r.fts5(q["q"]), pct), 1.0))
    # the production fusion: score-gate AND up-weight the surviving FTS5 hits
    for pct in (75, 90):
        out[f"gated_p{pct}_w2"] = score_set(
            qs, accs, qvecs, r,
            lambda q, v, pct=pct: wrrf(r.dense(v), gate_top(r.fts5(q["q"]), pct), 2.0))
    print(f"\n[{name}] n={qs and len(qs)}")
    for k, val in out.items():
        if k.startswith("_"):
            continue
        print(f"  {k:14} R@1={val['recall@1']:.3f} R@5={val['recall@5']:.3f}")
    return out


def main():
    index_config, query_config = EVALS[EVALCONFIG]
    r = Retriever(index_config, VARIANT, Embedder(query_config, verbose=False))

    # exact-extraction probe (new queries -> embedded via server)
    probe = json.load(open(os.path.join(DATA, "probe_exact.json")))["questions"]
    pemb = Embedder(query_config, verbose=False).embed([q["q"] for q in probe], role="query")
    pvec = {q["id"]: pemb[i] for i, q in enumerate(probe)}
    pacc = {q["id"]: set(q["acc"]) for q in probe}

    # clean paraphrase questions (cached query vectors)
    qv = embed_queries(query_config)
    QS = json.load(open(os.path.join(DATA, "eval_questions.json")))["questions"]
    ANS = {int(k): set(v) for k, v in json.load(open(os.path.join(DATA, "answer_sets.json"))).items()}
    clean = [q for q in QS if not q["not_found"] and not q.get("misspelled")
             and q.get("category") not in ("exact", "exact_llm")]
    cacc = {q["id"]: ANS.get(q["id"], {q["support_sent"]}) for q in clean}

    out = {"variant": VARIANT, "weights": WEIGHTS,
           "exact": run_set("exact-probe", probe, pacc, pvec, r),
           "clean": run_set("clean-paraphrase", clean, cacc, qv, r)}
    json.dump(out, open(os.path.join(DATA, f"{RP}_probe_sweep.json"), "w"), indent=1)
    print(f"\nwrote data/{RP}_probe_sweep.json")


if __name__ == "__main__":
    main()
