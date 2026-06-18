#!/usr/bin/env python3
"""
Driver for the LLM-in-the-loop experiments (chat server on :2234 required;
granite embedder on :2235; the qwen embedder is NOT needed at query time because
its chunk + query vectors are already cached/stored).

  python3 r2_llm_run.py hyde         # HyDE query expansion (granite)
  python3 r2_llm_run.py rerank       # LLM rerank of top candidates
  python3 r2_llm_run.py contextual   # Contextual Retrieval index + eval
  python3 r2_llm_run.py geneval      # generation-grounded LLM-judge (subset)
Results append to data/r2_results_llm.json.
"""
import json
import os
import sys

import numpy as np

from lib import DATA, ChatLLM, Embedder
import r2_eval as E
from r2_eval import evaluate, embed_queries, INDOC, OOD, ANS, BUDGETS
from r2_retrieval import Retriever
import r2_llm


def _save(key, payload):
    # one file per key so concurrent drivers don't clobber a shared json
    path = os.path.join(DATA, f"r2_llm_{key}.json")
    json.dump({key: payload}, open(path, "w"), indent=1)
    print("saved", key, "->", path, file=sys.stderr)


def run_hyde():
    llm = ChatLLM()
    hv = r2_llm.hyde_vectors("granite", llm)
    res = []
    for v in ["sem_adjacent_g0.12", "structure_1024", "mech_256", "per_sentence"]:
        r = evaluate(f"granite/{v}/hyde", "granite", v, "dense", hv)
        res.append(r)
        print(f"HyDE granite/{v}: R@1={r['recall@1']:.3f} R@5={r['recall@5']:.3f}", file=sys.stderr)
    _save("hyde", res)


def run_rerank():
    """Rerank top-50 candidates of the best static pipelines with the LLM.
    Scored on the fixed subset to bound CPU cost (long 10-passage prompts)."""
    llm = ChatLLM()
    sub = set(json.load(open(os.path.join(DATA, "eval_subset.json")))["indoc"])
    SUBQ = [q for q in INDOC if q["id"] in sub]
    qv_g = embed_queries("granite")
    qv_q = embed_queries("qwen4b")
    targets = [("qwen4b", "structure_1024", "dense", qv_q),   # best static -> +rerank
               ("granite", "mech_1024", "dense", qv_g)]       # weak (v1-like) -> rescued
    res = []
    for backend, variant, mode, qv in targets:
        r = Retriever(backend, variant, embedder=qv["_emb"], with_bm25=(mode != "dense"))
        fhr, base_fhr = [], []
        for q in SUBQ:
            if mode == "hybrid":
                cand = r.hybrid(qv[q["id"]], q["q"], k=50, depth=50)
            else:
                cand = r.dense(qv[q["id"]], 50)
            acc = ANS.get(q["id"], {q["support_sent"]})
            base_fhr.append(E._first_hit_rank(cand, r.by_id, acc))   # pre-rerank
            rer = r2_llm.llm_rerank(q["q"], cand, r.by_id, llm, k=10)
            fhr.append(E._first_hit_rank(rer, r.by_id, acc))
        def rec(fh, k): return float(np.mean([1.0 if (x and x <= k) else 0.0 for x in fh]))
        row = {"label": f"{backend}/{variant}/{mode}", "n": len(SUBQ),
               "base_recall@1": rec(base_fhr, 1), "base_recall@5": rec(base_fhr, 5),
               "rerank_recall@1": rec(fhr, 1), "rerank_recall@5": rec(fhr, 5),
               "rerank_mrr@10": float(np.mean([1.0/x if x else 0.0 for x in fhr]))}
        res.append(row)
        print(f"RERANK {row['label']}: R@1 {row['base_recall@1']:.3f}->{row['rerank_recall@1']:.3f} "
              f"R@5 {row['base_recall@5']:.3f}->{row['rerank_recall@5']:.3f}", file=sys.stderr)
    _save("rerank", res)


def run_contextual():
    llm = ChatLLM()
    res = []
    for base in ["sem_adjacent_g0.12", "mech_256"]:
        name = r2_llm.build_contextual_index(base, "granite", llm)
        qv = embed_queries("granite")
        r = evaluate(f"granite/{name}/dense", "granite", name, "dense", qv)
        res.append(r)
        print(f"CTX granite/{name}: R@1={r['recall@1']:.3f} R@5={r['recall@5']:.3f} "
              f"ans@1024={r['answer@1024tok']:.3f}", file=sys.stderr)
    _save("contextual", res)


def run_geneval():
    llm = ChatLLM()
    sub = json.load(open(os.path.join(DATA, "eval_subset.json")))["indoc"][:120]
    qv_g = embed_queries("granite"); qv_q = embed_queries("qwen4b")
    pipes = [("granite", "mech_1024", "dense", qv_g),         # v1-style baseline
             ("qwen4b", "structure_1024", "dense", qv_q),     # best static
             ("granite", "structure_1024", "hybrid", qv_g)]   # strong CPU-only
    res = []
    for backend, variant, mode, qv in pipes:
        acc = r2_llm.gen_eval(f"{backend}/{variant}/{mode}", backend, variant, mode,
                              qv, llm, sub, k=5)
        res.append({"label": f"{backend}/{variant}/{mode}", "gen_correct@5": acc})
        print(f"GENEVAL {backend}/{variant}/{mode}: {acc:.3f}", file=sys.stderr)
    _save("geneval", res)


if __name__ == "__main__":
    {"hyde": run_hyde, "rerank": run_rerank, "contextual": run_contextual,
     "geneval": run_geneval}[sys.argv[1]]()
