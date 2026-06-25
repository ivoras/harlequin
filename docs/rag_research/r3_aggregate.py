#!/usr/bin/env python3
"""
Aggregate the per-eval-config result files into the datasets paper3 renders.

Pipeline of decisions (each recorded for the paper):
  1. prefix gap   native vs no-prefix on a reference dense chunker -> carried mode
  2. gate sweep   carried mode, dense, sem_adjacent gate vs metrics -> best gate
  3. chunkers     carried mode, dense, 8 chunkers (size sweep + best-gate semantic)
  4. lexical      carried mode, best chunker, BM25 vs FTS5 (standalone + RRF hybrid)
  5. selection    each model's best (chunker, lexical) by a pinpoint+rejection
                  composite; models ranked by the same composite; cost table

Writes data/r3_agg.json consumed by make_paper3.py.
"""
import json
import os

import numpy as np

from lib import DATA, EMBEDDERS, MODELS, MODES_OF

RP = os.environ.get("RPREFIX", "r3")     # result-file prefix (r3 = 802-set, r4 = augmented)
REF_CHUNKER = "mech_512"          # neutral mid-size dense reference for the prefix gap
# pinpoint + rejection weighted composite (per the study's objective)
COMPOSITE_W = {"recall@1": 2.0, "mrr@10": 2.0, "ood_auc": 2.0,
               "recall@5": 1.0, "answer@1024tok": 1.0}


def load_results(evalconfig):
    p = os.path.join(DATA, f"{RP}_results_{evalconfig}.json")
    if not os.path.exists(p):
        return None
    d = json.load(open(p))
    return {(r["variant"], r["mode"]): r for r in d["results"]}


def manifest(index_config):
    return json.load(open(os.path.join(DATA, "r3idx", index_config, "manifest.json")))


def composite(rows):
    """Weighted mean of per-metric z-scores across `rows` (list of metric dicts).
    Higher = better. Returns a parallel list of scores."""
    keys = list(COMPOSITE_W)
    mat = np.array([[r.get(k, np.nan) for k in keys] for r in rows], dtype=float)
    z = np.zeros_like(mat)
    for j in range(mat.shape[1]):
        col = mat[:, j]
        mu, sd = np.nanmean(col), np.nanstd(col)
        z[:, j] = 0.0 if sd < 1e-9 else (col - mu) / sd
    w = np.array([COMPOSITE_W[k] for k in keys])
    return list((np.nansum(z * w, axis=1) / w.sum()))


HEADLINE = ["recall@1", "recall@5", "recall@10", "answer@256tok", "answer@1024tok",
            "mrr@10", "recall@5_misspelled", "recall@5_dumb", "ood_auc", "eer"]


def slim(r):
    out = {k: r.get(k) for k in HEADLINE}
    out["n_chunks"] = r.get("n_chunks")
    return out


def main():
    bench = json.load(open(os.path.join(DATA, f"{RP}_bench.json")))
    agg = {"models": {}, "prefix_gap": [], "gate": {}, "chunkers": {},
           "lexical": {}, "selection": [], "ref_chunker": REF_CHUNKER,
           "composite_w": COMPOSITE_W, "naming": {}}

    # results per eval-config that exists
    res = {}
    for m in MODELS:
        for mode in MODES_OF[m]:
            r = load_results(mode)
            if r is not None:
                res[mode] = r

    carried = {}
    for m in MODELS:
        modes = [mo for mo in MODES_OF[m] if mo in res]
        if not modes:
            continue
        # ---- 1. prefix gap on the reference dense chunker ----
        gap_rows = []
        for mo in modes:
            r = res[mo].get((REF_CHUNKER, "dense"))
            if r:
                gap_rows.append({"model": m, "mode": mo,
                                 "native": not mo.endswith("_np"), **slim(r)})
        if not gap_rows:
            continue
        if len(gap_rows) > 1:
            for row, sc in zip(gap_rows, composite(gap_rows)):
                row["composite"] = sc
            best = max(gap_rows, key=lambda x: x["composite"])["mode"]
        else:
            best = gap_rows[0]["mode"]
        carried[m] = best
        agg["prefix_gap"].extend(gap_rows)

        man = manifest(json_index_of(best))
        # ---- 2. gate sweep (carried mode, dense) ----
        gate_vars = sorted([v for (v, mo) in res[best] if mo == "dense"
                            and v.startswith("semadj_g")],
                           key=lambda v: float(v.split("_g")[1]))
        gate_rows = []
        for v in gate_vars:
            r = res[best][(v, "dense")]
            gate_rows.append({"gate": float(v.split("_g")[1]), "variant": v,
                              "tok_mean": man["variants"][v]["tok_mean"],
                              "recall@1": r["recall@1"], "recall@5": r["recall@5"],
                              "answer@1024tok": r["answer@1024tok"], "ood_auc": r["ood_auc"],
                              "mrr@10": r["mrr@10"]})
        if gate_rows:
            for row, sc in zip(gate_rows, composite(gate_rows)):
                row["composite"] = sc
            best_gate = max(gate_rows, key=lambda x: x["composite"])
            agg["gate"][m] = {"mode": best, "rows": gate_rows,
                              "best_variant": best_gate["variant"],
                              "best_gate": best_gate["gate"]}
        best_semadj = agg["gate"].get(m, {}).get("best_variant")

        # ---- 3. chunker comparison (carried mode, dense) ----
        chunk_order = ["per_sentence", "mech_256", "mech_512", "mech_1024", "mech_1500",
                       "overlap_512", "overlap_1024"]
        crows = []
        for v in chunk_order:
            r = res[best].get((v, "dense"))
            if r:
                crows.append({"chunker": v, "tok_mean": man["variants"][v]["tok_mean"],
                              **slim(r)})
        if best_semadj:
            r = res[best][(best_semadj, "dense")]
            crows.append({"chunker": f"sem_adjacent (g={float(best_semadj.split('_g')[1]):.3f})",
                          "variant": best_semadj,
                          "tok_mean": man["variants"][best_semadj]["tok_mean"], **slim(r)})
        agg["chunkers"][m] = crows

        # ---- 4. lexical backend (carried mode), best dense chunker as the base ----
        dense_candidates = chunk_order + ([best_semadj] if best_semadj else [])
        comp_rows = [res[best][(v, "dense")] for v in dense_candidates if (v, "dense") in res[best]]
        base_chunker = dense_candidates[int(np.argmax(composite(comp_rows)))]
        lex = []
        for mode_name in ["dense", "bm25", "fts5", "bm25_hybrid", "fts5_hybrid"]:
            r = res[best].get((base_chunker, mode_name))
            if r:
                lex.append({"lexical": mode_name, **slim(r)})
        agg["lexical"][m] = {"mode": best, "base_chunker": base_chunker, "rows": lex}

        # ---- 5. this model's best (chunker, lexical) by composite ----
        cand = []
        for v in dense_candidates:
            for mode_name in ["dense", "bm25", "fts5", "bm25_hybrid", "fts5_hybrid"]:
                r = res[best].get((v, mode_name))
                if r:
                    cand.append((v, mode_name, r))
        scores = composite([c[2] for c in cand])
        bi = int(np.argmax(scores))
        bv, bmode, br = cand[bi]
        b = bench.get(__server_of(m), {})
        agg["selection"].append({
            "model": m, "mode": best, "native": not best.endswith("_np"),
            "chunker": bv, "lexical": bmode, "label": f"{best}/{bv}/{bmode}",
            "dim": EMBEDDERS[best]["dim"], "n_chunks": br.get("n_chunks"),
            "vec_per_sec": b.get("vec_per_sec"), "tok_per_sec": b.get("tok_per_sec"),
            **slim(br)})

    # cross-model composite + rank on the representatives
    if agg["selection"]:
        for row, sc in zip(agg["selection"], composite(agg["selection"])):
            row["composite"] = sc
        agg["selection"].sort(key=lambda x: -x["composite"])
        for i, row in enumerate(agg["selection"], 1):
            row["rank"] = i

    agg["carried"] = carried
    agg["naming"] = {m: {"query_prefix": EMBEDDERS[carried[m]]["query_prefix"],
                         "doc_prefix": EMBEDDERS[carried[m]]["doc_prefix"]}
                     for m in carried}
    json.dump(agg, open(os.path.join(DATA, f"{RP}_agg.json"), "w"), indent=1)
    print(f"wrote data/{RP}_agg.json; carried modes:", carried)


# the index_config a carried eval-config reads (mirror of r3_eval.EVALS)
def json_index_of(evalconfig):
    base = evalconfig[:-3] if evalconfig.endswith("_np") else evalconfig
    # snowflake/qwen no-prefix reuse the native index; gemma/lfm2 have their own
    if evalconfig in ("snowflake_np", "qwen06b_np"):
        return base
    return evalconfig


def __server_of(model):
    return model


if __name__ == "__main__":
    main()
