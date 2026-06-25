#!/usr/bin/env python3
"""
Evaluate one eval-config (model + prompt mode) over all its index variants and
retrieval modes, writing data/r3_results_{evalconfig}.json.

An eval-config separates the index it reads from the query embedder it uses, so
the no-prefix arm of a model whose document prefix is empty (snowflake, qwen)
reuses the native index and only swaps the query prompt — no rebuild needed.

Retrieval modes per variant: dense (sqlite-vec cosine), bm25 (custom Okapi),
fts5 (SQLite FTS5 built-in BM25), and the dense+lexical RRF hybrids of each.
Metrics are duplicate-aware, size-normalised (answer@B tokens) with bootstrap
CIs and OOD separability, identical in definition to paper2's r2_eval.

Usage:  python3 r3_eval.py <evalconfig>     e.g. granite | snowflake_np | gemma
"""
import json
import os
import sqlite3
import sys

import numpy as np

from bm25 import BM25, tokenize
from lib import DATA, Embedder, VectorStore
from r3_build import idx_path

QS = json.load(open(os.path.join(DATA, "eval_questions.json")))["questions"]
ANS = {int(k): set(v) for k, v in json.load(open(os.path.join(DATA, "answer_sets.json"))).items()}
INDOC = [q for q in QS if not q["not_found"]]
OOD = [q for q in QS if q["not_found"]]
BUDGETS = (256, 1024)
DEPTH = 50
RNG = np.random.default_rng(0)

# eval-config -> (index_config, query_config). Index reuse where doc prefix is
# empty in both native and no-prefix modes (snowflake, qwen06b).
EVALS = {
    "granite":      ("granite", "granite"),
    "snowflake":    ("snowflake", "snowflake"),
    "snowflake_np": ("snowflake", "snowflake_np"),
    "qwen06b":      ("qwen06b", "qwen06b"),
    "qwen06b_np":   ("qwen06b", "qwen06b_np"),
    "gemma":        ("gemma", "gemma"),
    "gemma_np":     ("gemma_np", "gemma_np"),
    "lfm2":         ("lfm2", "lfm2"),
    "lfm2_np":      ("lfm2_np", "lfm2_np"),
}


# ---- RRF + metric helpers (definitions match r2_eval) ----
def rrf(*lists, k=DEPTH, c=60):
    agg = {}
    for lst in lists:
        for rank, (cid, _s) in enumerate(lst, start=1):
            agg[cid] = agg.get(cid, 0.0) + 1.0 / (c + rank)
    return sorted(agg.items(), key=lambda x: -x[1])[:k]


def _covers(ch, acc):
    return any(ch["sent_start"] <= a <= ch["sent_end"] for a in acc)


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


def _ci(vals, n_boot=1000):
    a = np.asarray(vals, dtype=np.float32)
    if len(a) == 0:
        return (0.0, 0.0, 0.0)
    idx = RNG.integers(0, len(a), size=(n_boot, len(a)))
    m = a[idx].mean(axis=1)
    return float(a.mean()), float(np.percentile(m, 2.5)), float(np.percentile(m, 97.5))


def _auc(pos, neg):
    pos, neg = np.asarray(pos), np.asarray(neg)
    if len(pos) == 0 or len(neg) == 0:
        return float("nan")
    allv = np.concatenate([pos, neg])
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


class Retriever:
    def __init__(self, index_config, variant, qemb):
        self.qemb = qemb
        self.store = VectorStore(idx_path(index_config, variant), dim=qemb.dim)
        self.chunks = self.store.all_chunks()
        self.ids = [c["id"] for c in self.chunks]
        self.by_id = {c["id"]: c for c in self.chunks}
        self.bm25 = BM25([c["text"] for c in self.chunks])
        self.fts = sqlite3.connect(":memory:")
        self.fts.execute("CREATE VIRTUAL TABLE fts USING fts5(text)")
        self.fts.executemany("INSERT INTO fts(rowid, text) VALUES (?, ?)",
                             [(c["id"], c["text"]) for c in self.chunks])

    def dense(self, qvec, k=DEPTH):
        return self.store.search(qvec, k)

    def lexical(self, qtext, k=DEPTH):
        return [(self.ids[i], s) for i, s in self.bm25.topk(qtext, k)]

    def fts5(self, qtext, k=DEPTH):
        terms = tokenize(qtext)
        if not terms:
            return []
        match = " OR ".join('"%s"' % t.replace('"', "") for t in terms)
        rows = self.fts.execute(
            "SELECT rowid, bm25(fts) FROM fts WHERE fts MATCH ? ORDER BY bm25(fts) LIMIT ?",
            (match, k)).fetchall()
        return [(int(rid), -float(b)) for rid, b in rows]

    def retrieve(self, mode, qvec, qtext):
        if mode == "dense":
            return self.dense(qvec)
        if mode == "bm25":
            return self.lexical(qtext)
        if mode == "fts5":
            return self.fts5(qtext)
        if mode == "bm25_hybrid":
            return rrf(self.dense(qvec), self.lexical(qtext))
        if mode == "fts5_hybrid":
            return rrf(self.dense(qvec), self.fts5(qtext))
        raise ValueError(mode)


def evaluate(evalconfig, index_config, variant, mode, qvecs):
    r = Retriever(index_config, variant, qvecs["_emb"])
    by_id = r.by_id
    fhr, top1_indoc = [], []
    budget_hits = {b: [] for b in BUDGETS}
    grp = {"misspelled": [], "dumb": [], "normal": []}
    for q in INDOC:
        ranked = r.retrieve(mode, qvecs[q["id"]], q["q"])
        acc = ANS.get(q["id"], {q["support_sent"]})
        rank = _first_hit_rank(ranked, by_id, acc)
        fhr.append(rank)
        top1_indoc.append(ranked[0][1] if ranked else -1e9)
        for b in BUDGETS:
            budget_hits[b].append(_hit_within_budget(ranked, by_id, acc, b))
        g = "dumb" if q["category"] == "dumb" else ("misspelled" if q["misspelled"] else "normal")
        grp[g].append(1.0 if (rank and rank <= 5) else 0.0)
    top1_ood = [(r.retrieve(mode, qvecs[q["id"]], q["q"]) or [(0, -1e9)])[0][1] for q in OOD]

    def rec_at(k):
        return [1.0 if (rr and rr <= k) else 0.0 for rr in fhr]
    res = {"label": f"{evalconfig}/{variant}/{mode}", "evalconfig": evalconfig,
           "variant": variant, "mode": mode, "n_chunks": len(r.ids)}
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


def embed_queries(query_config):
    emb = Embedder(query_config, verbose=False)
    V = emb.embed([q["q"] for q in QS], role="query")
    d = {q["id"]: V[i] for i, q in enumerate(QS)}
    d["_emb"] = emb
    return d


def variants_for(index_config):
    manifest = json.load(open(os.path.join(DATA, "r3idx", index_config, "manifest.json")))
    return list(manifest["variants"].keys())


def main():
    evalconfig = sys.argv[1] if len(sys.argv) > 1 else "granite"
    index_config, query_config = EVALS[evalconfig]
    qv = embed_queries(query_config)
    variants = variants_for(index_config)
    # dense over every variant (size sweep + gate sweep); lexical/hybrid modes too
    # so the lexical-backend chapter has BM25 vs FTS5 across chunkers.
    modes = ["dense", "bm25", "fts5", "bm25_hybrid", "fts5_hybrid"]
    results = []
    for v in variants:
        for mode in modes:
            try:
                results.append(evaluate(evalconfig, index_config, v, mode, qv))
            except Exception as ex:  # noqa: BLE001
                print(f"SKIP {evalconfig}/{v}/{mode}: {ex}", file=sys.stderr)
        r = next((x for x in results if x["variant"] == v and x["mode"] == "dense"), None)
        if r:
            print(f"{evalconfig}/{v:18} dense R@1={r['recall@1']:.3f} "
                  f"R@5={r['recall@5']:.3f} ans@1024={r['answer@1024tok']:.3f} "
                  f"AUC={r['ood_auc']:.3f}", file=sys.stderr)
    out = os.path.join(DATA, f"r3_results_{evalconfig}.json")
    json.dump({"evalconfig": evalconfig, "index_config": index_config,
               "query_config": query_config, "results": results}, open(out, "w"), indent=1)
    print("wrote", out, file=sys.stderr)


if __name__ == "__main__":
    main()
