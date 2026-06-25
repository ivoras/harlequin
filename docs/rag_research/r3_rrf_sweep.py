#!/usr/bin/env python3
"""
RRF lexical-weight sweep on snowflake/semadj_g0.4341/fts5_hybrid.

Fuses the dense (sqlite-vec) and FTS5 lexical arms with weighted Reciprocal Rank
Fusion, holding the dense arm at weight 1.0 and sweeping the FTS5 arm's weight.
Because FTS5 is exact-token, more lexical weight should help well-formed queries
and hurt misspelled ones; we therefore score the misspelled and clean (non-
misspelled) in-document subsets separately. Query vectors are cached from the
main eval, so this runs offline. Writes data/r3_rrf_sweep.json.
"""
import json
import os

from r3_eval import (ANS, DATA, EVALS, INDOC, Retriever, _first_hit_rank,
                     embed_queries)

EVALCONFIG = "snowflake"            # snowflake's carried prompt mode (native)
VARIANT = "semadj_g0.4341"
WEIGHTS = [0.25, 0.5, 1.0, 2.0, 4.0]   # FTS5-arm RRF weight (dense fixed at 1.0)
RRF_K = 60


def wrrf(dense, fts, w_fts, k=RRF_K):
    """Weighted RRF: dense arm weight 1.0, FTS5 arm weight w_fts."""
    agg = {}
    for rank, (cid, _s) in enumerate(dense, 1):
        agg[cid] = agg.get(cid, 0.0) + 1.0 / (k + rank)
    for rank, (cid, _s) in enumerate(fts, 1):
        agg[cid] = agg.get(cid, 0.0) + w_fts / (k + rank)
    return sorted(agg.items(), key=lambda x: -x[1])


def main():
    index_config, query_config = EVALS[EVALCONFIG]
    qv = embed_queries(query_config)
    r = Retriever(index_config, VARIANT, qv["_emb"])
    subsets = {
        "misspelled": [q for q in INDOC if q.get("misspelled")],
        "clean": [q for q in INDOC if not q.get("misspelled")],
    }
    # precompute each query's dense + fts ranked lists once (weight-independent)
    pre = {}
    for q in INDOC:
        pre[q["id"]] = (r.dense(qv[q["id"]]), r.fts5(q["q"]))

    rows = []
    for w in WEIGHTS:
        rec = {"w_fts": w}
        for name, qs in subsets.items():
            r1 = r5 = mrr = 0
            for q in qs:
                dense, fts = pre[q["id"]]
                ranked = wrrf(dense, fts, w)
                acc = ANS.get(q["id"], {q["support_sent"]})
                rank = _first_hit_rank(ranked, r.by_id, acc)
                r1 += rank == 1
                r5 += bool(rank and rank <= 5)
                mrr += (1.0 / rank) if rank else 0.0
            n = len(qs)
            rec[name] = {"n": n, "recall@1": r1 / n, "recall@5": r5 / n, "mrr@10": mrr / n}
        rows.append(rec)
        print(f"w_fts={w:<5} clean R@1={rec['clean']['recall@1']:.3f} R@5={rec['clean']['recall@5']:.3f}"
              f"  mis R@1={rec['misspelled']['recall@1']:.3f} R@5={rec['misspelled']['recall@5']:.3f}")
    out = {"evalconfig": EVALCONFIG, "variant": VARIANT, "weights": WEIGHTS,
           "base_weight": 1.0, "rrf_k": RRF_K, "rows": rows}
    json.dump(out, open(os.path.join(DATA, "r3_rrf_sweep.json"), "w"), indent=1)
    print("wrote data/r3_rrf_sweep.json")


if __name__ == "__main__":
    main()
