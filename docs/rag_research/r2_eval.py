#!/usr/bin/env python3
"""
Rework evaluator. Duplicate-aware hit scoring, size-normalised (equal-token-
budget) metrics, OOD separability, and bootstrap confidence intervals.

A retrieved chunk is a HIT if its sentence span covers ANY acceptable sentence
for the question (data/answer_sets.json). Metrics:
  answer_recall@k        any top-k chunk is a hit
  mrr@10                 first hit rank
  answer@{B}tok          hit within the top chunks whose cumulative tokens <= B
                         (compares methods at equal retrieved-context cost)
  recall@5 by subgroup   misspelled / dumb
  ood_auc, eer           top-1 score separates in-doc vs out-of-domain
All headline metrics carry a 95% bootstrap CI over questions.

CLI: python3 r2_eval.py core   |  confound  |  ablation
"""
import json
import os
import sys

import numpy as np

from lib import DATA, Embedder
from r2_retrieval import Retriever, rrf

QS = json.load(open(os.path.join(DATA, "eval_questions.json")))["questions"]
ANS = {int(k): set(v) for k, v in json.load(open(os.path.join(DATA, "answer_sets.json"))).items()}
INDOC = [q for q in QS if not q["not_found"]]
OOD = [q for q in QS if q["not_found"]]
BUDGETS = (256, 1024)
RNG = np.random.default_rng(0)


def _covers(chunk, acc):
    return any(chunk["sent_start"] <= a <= chunk["sent_end"] for a in acc)


def _first_hit_rank(ranked, by_id, acc):
    for rank, (cid, _s) in enumerate(ranked, start=1):
        if _covers(by_id[cid], acc):
            return rank
    return None


def _hit_within_budget(ranked, by_id, acc, budget):
    tok = 0
    for cid, _s in ranked:
        ch = by_id[cid]
        tok += ch["n_tok"]
        if _covers(ch, acc):
            return True
        if tok >= budget:
            return False
    return False


def _ci(per_q_bool, n_boot=1000):
    """95% bootstrap CI of a mean over questions."""
    a = np.asarray(per_q_bool, dtype=np.float32)
    if len(a) == 0:
        return (0.0, 0.0, 0.0)
    idx = RNG.integers(0, len(a), size=(n_boot, len(a)))
    means = a[idx].mean(axis=1)
    return float(a.mean()), float(np.percentile(means, 2.5)), float(np.percentile(means, 97.5))


def _auc(pos, neg):
    pos, neg = np.asarray(pos), np.asarray(neg)
    if len(pos) == 0 or len(neg) == 0:
        return float("nan")
    allv = np.concatenate([pos, neg])
    order = allv.argsort()
    ranks = np.empty(len(allv)); ranks[order] = np.arange(1, len(allv) + 1)
    _, inv, counts = np.unique(allv, return_inverse=True, return_counts=True)
    cum = np.cumsum(counts); avg = (cum - counts + cum + 1) / 2.0
    ranks = avg[inv]
    return float((ranks[:len(pos)].sum() - len(pos) * (len(pos) + 1) / 2) / (len(pos) * len(neg)))


def _eer(pos, neg):
    pos, neg = np.asarray(pos), np.asarray(neg)
    if len(pos) == 0 or len(neg) == 0:
        return float("nan")
    best, gap = 0.5, 9e9
    for t in np.unique(np.concatenate([pos, neg])):
        fpr = float((neg >= t).mean()); fnr = 1 - float((pos >= t).mean())
        if abs(fpr - fnr) < gap:
            gap, best = abs(fpr - fnr), (fpr + fnr) / 2
    return best


def evaluate(label, backend, variant, mode, qvecs, depth=50):
    r = Retriever(backend, variant, embedder=qvecs["_emb"], with_bm25=(mode != "dense"))
    by_id = r.by_id
    ranked_indoc, top1_indoc = [], []
    fhr, budget_hits = [], {b: [] for b in BUDGETS}
    grp = {"misspelled": [], "dumb": [], "normal": []}
    for q in INDOC:
        ranked = _retrieve(r, q, qvecs, mode, depth)
        acc = ANS.get(q["id"], {q["support_sent"]})
        rank = _first_hit_rank(ranked, by_id, acc)
        fhr.append(rank)
        top1_indoc.append(ranked[0][1] if ranked else -1e9)
        for b in BUDGETS:
            budget_hits[b].append(_hit_within_budget(ranked, by_id, acc, b))
        g = "dumb" if q["category"] == "dumb" else ("misspelled" if q["misspelled"] else "normal")
        grp[g].append(1.0 if (rank and rank <= 5) else 0.0)
    top1_ood = []
    for q in OOD:
        ranked = _retrieve(r, q, qvecs, mode, depth)
        top1_ood.append(ranked[0][1] if ranked else -1e9)

    def rec_at(k):
        return [1.0 if (rr and rr <= k) else 0.0 for rr in fhr]
    res = {"label": label, "backend": backend, "variant": variant, "mode": mode,
           "n_chunks": len(r.ids)}
    for k in (1, 3, 5, 10):
        m, lo, hi = _ci(rec_at(k)); res[f"recall@{k}"] = m
        if k in (1, 5):
            res[f"recall@{k}_ci"] = [lo, hi]
    res["mrr@10"] = float(np.mean([1.0 / rr if rr else 0.0 for rr in fhr]))
    for b in BUDGETS:
        m, lo, hi = _ci(budget_hits[b]); res[f"answer@{b}tok"] = m; res[f"answer@{b}tok_ci"] = [lo, hi]
    for g in grp:
        res[f"recall@5_{g}"] = float(np.mean(grp[g])) if grp[g] else None
    res["ood_auc"] = _auc(top1_indoc, top1_ood)
    res["eer"] = _eer(top1_indoc, top1_ood)
    return res


def _retrieve(r, q, qvecs, mode, depth):
    if mode == "dense":
        return r.dense(qvecs[q["id"]], depth)
    if mode == "bm25":
        return r.lexical(q["q"], depth)
    if mode == "hybrid":
        return r.hybrid(qvecs[q["id"]], q["q"], k=depth, depth=depth)
    raise ValueError(mode)


def embed_queries(backend):
    emb = Embedder(backend, verbose=False)
    texts = [q["q"] for q in QS]
    V = emb.embed(texts, role="query")
    d = {q["id"]: V[i] for i, q in enumerate(QS)}
    d["_emb"] = emb
    return d


CORE_VARIANTS = ["per_sentence", "sem_adjacent_g0.12", "structure_1024",
                 "mech_256", "mech_512", "mech_1024", "mech_1500", "overlap_1024"]


def configs_for(group):
    if group == "core":
        c = [("granite/"+v+"/dense", "granite", v, "dense") for v in CORE_VARIANTS]
        return c
    if group == "ablation":
        c = []
        for v in CORE_VARIANTS:
            c.append((f"granite/{v}/dense", "granite", v, "dense"))
            c.append((f"granite/{v}/bm25", "granite", v, "bm25"))
            c.append((f"granite/{v}/hybrid", "granite", v, "hybrid"))
            c.append((f"qwen4b/{v}/dense", "qwen4b", v, "dense"))
            c.append((f"qwen4b/{v}/hybrid", "qwen4b", v, "hybrid"))
        return c
    if group == "confound":
        idxdir = os.path.join(DATA, "idx", "granite")
        vs = sorted(f[:-7] for f in os.listdir(idxdir) if f.endswith(".sqlite"))
        vs = [v for v in vs if v.startswith(("fixed_", "random_", "semadj_", "semcen_"))]
        return [(f"granite/{v}/dense", "granite", v, "dense") for v in vs]
    raise ValueError(group)


def main():
    group = sys.argv[1] if len(sys.argv) > 1 else "core"
    cfgs = configs_for(group)
    backends = sorted({b for _, b, _, _ in cfgs})
    qv = {b: embed_queries(b) for b in backends}
    results = []
    for label, backend, variant, mode in cfgs:
        try:
            results.append(evaluate(label, backend, variant, mode, qv[backend]))
            r = results[-1]
            print(f"{label:38} R@1={r['recall@1']:.3f} R@5={r['recall@5']:.3f} "
                  f"ans@1024={r['answer@1024tok']:.3f} AUC={r['ood_auc']:.3f}", file=sys.stderr)
        except Exception as ex:  # noqa: BLE001
            print(f"SKIP {label}: {ex}", file=sys.stderr)
    out = os.path.join(DATA, f"r2_results_{group}.json")
    json.dump({"group": group, "results": results}, open(out, "w"), indent=1)
    print("wrote", out, file=sys.stderr)


if __name__ == "__main__":
    main()
