#!/usr/bin/env python3
"""
Exact-extraction probe sweep + FTS5 score-gating experiment.

On two query sets — the exact-extraction probe (data/probe_exact.json) and the
clean (non-misspelled) paraphrase questions — compare on
snowflake/semadj_g0.4341:
  * dense only
  * dense+FTS5 RRF at several FTS5-arm weights
  * dense+FTS5 RRF where weak FTS5 hits (BM25 score below an absolute floor) are
    dropped before fusion (the "score-gated hybrid")

The gate keeps only confident lexical matches (rare exact tokens earn high BM25),
so it should add lexical signal on exact queries without polluting paraphrase
ones. Needs the snowflake server on :2235 (probe queries are new). Writes
data/r3_probe_sweep.json.
"""
import json
import os

import numpy as np

from lib import DATA, Embedder
from r3_eval import EVALS, Retriever, _first_hit_rank, embed_queries
from r3_rrf_sweep import EVALCONFIG, VARIANT, wrrf

RP = os.environ.get("RPREFIX", "r3")
WEIGHTS = [0.25, 1.0, 2.0, 4.0]
GATE_PCTS = [0, 50, 75, 90]            # absolute BM25 floor at these score percentiles


def score_set(qs, accs, qvecs, r, rank_fn):
    r1 = r5 = 0
    for q in qs:
        ranked = rank_fn(q, qvecs[q["id"]])
        rank = _first_hit_rank(ranked, r.by_id, accs[q["id"]])
        r1 += rank == 1
        r5 += bool(rank and rank <= 5)
    n = len(qs)
    return {"n": n, "recall@1": r1 / n, "recall@5": r5 / n}


def run_set(name, qs, accs, qvecs, r, floors):
    out = {}
    out["dense"] = score_set(qs, accs, qvecs, r,
                             lambda q, v: r.dense(v))
    for w in WEIGHTS:
        out[f"hybrid_w{w}"] = score_set(qs, accs, qvecs, r,
                                        lambda q, v, w=w: wrrf(r.dense(v), r.fts5(q["q"]), w))
    for pct, T in floors.items():
        out[f"gated_p{pct}"] = score_set(
            qs, accs, qvecs, r,
            lambda q, v, T=T: wrrf(r.dense(v), [(c, s) for c, s in r.fts5(q["q"]) if s >= T], 1.0))
    out["_floors"] = floors
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

    # BM25 score floors from the probe queries' FTS5 score distribution
    allscores = []
    for q in probe:
        allscores += [s for _, s in r.fts5(q["q"])]
    floors = {p: float(np.percentile(allscores, p)) if allscores else 0.0 for p in GATE_PCTS}

    out = {"variant": VARIANT, "weights": WEIGHTS, "floors": floors,
           "exact": run_set("exact-probe", probe, pacc, pvec, r, floors),
           "clean": run_set("clean-paraphrase", clean, cacc, qv, r, floors)}
    json.dump(out, open(os.path.join(DATA, f"{RP}_probe_sweep.json"), "w"), indent=1)
    print(f"\nwrote data/{RP}_probe_sweep.json")


if __name__ == "__main__":
    main()
