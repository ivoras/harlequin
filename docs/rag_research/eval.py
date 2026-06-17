#!/usr/bin/env python3
"""
Evaluate every index variant against the eval question set and write a
comparison report (data/eval_results.json) plus a printed table.

Correctness model
-----------------
Every chunk records the global sentence-id span it covers (sent_start..sent_end)
and every in-document question records the id of the sentence that supports its
answer. A retrieved chunk is a HIT iff that support sentence lies inside the
chunk's span. This is exact and independent of how the corpus was chunked.

Metrics (per variant)
---------------------
recall@k           fraction of in-doc questions with >=1 hit in the top-k.
mrr@10             mean reciprocal rank of the first hit.
page_recall@5      looser: any top-5 chunk covers the answer's PDF page.
loc_tokens         content-localization precision: mean token size of the
                   first hit chunk (smaller = the answer is pinned more
                   tightly). loc_concentration = answer_sent_tokens / hit_tokens.
recall@5 by group  normal / misspelled / dumb breakdowns.
False positives (answerable vs out-of-domain)
   top-1 cosine similarity is the retrieval confidence. We measure how well it
   separates in-doc (answerable) from out-of-domain (unanswerable):
   auc, equal-error-rate (eer) + its threshold, and the false-positive rate
   at a threshold tuned to keep 95% of in-doc queries (fpr_at_95tpr).
"""
import json
import os
import sys

import numpy as np

from lib import DATA, Embedder, VectorStore, load_corpus, segment_corpus

IDX_DIR = os.path.join(DATA, "indexes")
KS = (1, 3, 5, 10)


def auc(pos: np.ndarray, neg: np.ndarray) -> float:
    """AUC that positives (in-doc) score higher than negatives (ood)."""
    if len(pos) == 0 or len(neg) == 0:
        return float("nan")
    allv = np.concatenate([pos, neg])
    order = allv.argsort()
    ranks = np.empty_like(order, dtype=float)
    ranks[order] = np.arange(1, len(allv) + 1)
    # average ranks for ties
    _, inv, counts = np.unique(allv, return_inverse=True, return_counts=True)
    cum = np.cumsum(counts)
    start = cum - counts
    avg = (start + cum + 1) / 2.0
    ranks = avg[inv]
    r_pos = ranks[:len(pos)].sum()
    return float((r_pos - len(pos) * (len(pos) + 1) / 2) / (len(pos) * len(neg)))


def eer_and_fpr(pos: np.ndarray, neg: np.ndarray):
    """Return (eer, eer_threshold, fpr_at_95tpr, thr95)."""
    if len(pos) == 0 or len(neg) == 0:
        return float("nan"), float("nan"), float("nan"), float("nan")
    thrs = np.unique(np.concatenate([pos, neg]))
    best_eer, best_thr = 1.0, float(thrs[0])
    fpr95, thr95 = 1.0, float(thrs[0])
    best_gap = 1e9
    for t in thrs:
        tpr = float((pos >= t).mean())
        fpr = float((neg >= t).mean())
        fnr = 1.0 - tpr
        if abs(fpr - fnr) < best_gap:
            best_gap = abs(fpr - fnr)
            best_eer = (fpr + fnr) / 2
            best_thr = float(t)
        if tpr >= 0.95 and fpr < fpr95:
            fpr95, thr95 = fpr, float(t)
    return best_eer, best_thr, fpr95, thr95


def evaluate(store: VectorStore, qs: list[dict], qvecs: np.ndarray) -> dict:
    indoc = [q for q in qs if not q["not_found"]]
    ood = [q for q in qs if q["not_found"]]
    qvec_by_id = {q["id"]: qvecs[i] for i, q in enumerate(qs)}

    hits_at = {k: 0 for k in KS}
    page_at5 = 0
    rr = 0.0
    loc_tokens, loc_conc = [], []
    grp = {"normal": [0, 0], "misspelled": [0, 0], "dumb": [0, 0]}
    indoc_top1 = []

    for q in indoc:
        res = store.search(qvec_by_id[q["id"]], k=10)
        indoc_top1.append(res[0][1] if res else -1.0)
        support = q["support_sent"]
        page = q["page"]
        first_hit_rank = None
        page_hit5 = False
        for rank, (cid, sim) in enumerate(res, start=1):
            ch = store.get_chunk(cid)
            is_hit = ch["sent_start"] <= support <= ch["sent_end"]
            if is_hit and first_hit_rank is None:
                first_hit_rank = rank
                loc_tokens.append(ch["n_tok"])
            if rank <= 5 and page in ch["pages"]:
                page_hit5 = True
        for k in KS:
            if first_hit_rank is not None and first_hit_rank <= k:
                hits_at[k] += 1
        if first_hit_rank is not None:
            rr += 1.0 / first_hit_rank
        if page_hit5:
            page_at5 += 1
        g = "dumb" if q["category"] == "dumb" else ("misspelled" if q["misspelled"] else "normal")
        grp[g][1] += 1
        if first_hit_rank is not None and first_hit_rank <= 5:
            grp[g][0] += 1

    ood_top1 = []
    for q in ood:
        res = store.search(qvec_by_id[q["id"]], k=10)
        ood_top1.append(res[0][1] if res else -1.0)

    n = len(indoc)
    pos = np.array(indoc_top1)
    neg = np.array(ood_top1)
    a = auc(pos, neg)
    eer, eer_thr, fpr95, thr95 = eer_and_fpr(pos, neg)

    return {
        "n_chunks": store.count(),
        **{f"recall@{k}": round(hits_at[k] / n, 4) for k in KS},
        "mrr@10": round(rr / n, 4),
        "page_recall@5": round(page_at5 / n, 4),
        "loc_tokens_mean": round(float(np.mean(loc_tokens)), 1) if loc_tokens else None,
        "recall@5_normal": round(grp["normal"][0] / max(grp["normal"][1], 1), 4),
        "recall@5_misspelled": round(grp["misspelled"][0] / max(grp["misspelled"][1], 1), 4),
        "recall@5_dumb": round(grp["dumb"][0] / max(grp["dumb"][1], 1), 4),
        "indoc_top1_sim_median": round(float(np.median(pos)), 4),
        "ood_top1_sim_median": round(float(np.median(neg)), 4),
        "ood_auc": round(a, 4),
        "eer": round(eer, 4),
        "eer_threshold": round(eer_thr, 4),
        "fpr_at_95tpr": round(fpr95, 4),
        "threshold_at_95tpr": round(thr95, 4),
    }


def main():
    data = json.load(open(os.path.join(DATA, "eval_questions.json")))
    qs = data["questions"]
    print(f"questions: {len(qs)} ({data['n_in_document']} in-doc, "
          f"{data['n_out_of_domain']} ood)", file=sys.stderr)

    emb = Embedder()
    qvecs = emb.embed([q["q"] for q in qs])
    print(f"embedded queries ({emb.calls} calls, {emb.cache_hits} cached)", file=sys.stderr)

    variants = sorted(f[:-7] for f in os.listdir(IDX_DIR) if f.endswith(".sqlite"))
    results = {}
    for name in variants:
        store = VectorStore(os.path.join(IDX_DIR, f"{name}.sqlite"))
        results[name] = evaluate(store, qs, qvecs)
        print(f"  {name}: R@1={results[name]['recall@1']} "
              f"R@5={results[name]['recall@5']} AUC={results[name]['ood_auc']}",
              file=sys.stderr)

    out = {"eval_set": data["source_document"],
           "n_in_document": data["n_in_document"],
           "n_out_of_domain": data["n_out_of_domain"],
           "results": results}
    with open(os.path.join(DATA, "eval_results.json"), "w") as f:
        json.dump(out, f, indent=2)

    # printed table
    cols = ["recall@1", "recall@3", "recall@5", "recall@10", "mrr@10",
            "page_recall@5", "loc_tokens_mean", "recall@5_misspelled",
            "recall@5_dumb", "ood_auc", "eer", "fpr_at_95tpr"]
    print("\n" + "variant".ljust(17) + "chunks".rjust(7) +
          "".join(c.replace("recall", "R").replace("@", "").rjust(11) for c in cols))
    for name in variants:
        r = results[name]
        row = name.ljust(17) + str(r["n_chunks"]).rjust(7)
        for c in cols:
            v = r[c]
            row += (f"{v:.4f}" if isinstance(v, float) else str(v)).rjust(11)
        print(row)


if __name__ == "__main__":
    main()
