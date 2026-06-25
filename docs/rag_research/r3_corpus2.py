#!/usr/bin/env python3
"""
Generalization replication for the winning model, end-to-end (one model the whole
path) on a contrasting non-legal corpus (Darwin, Origin of Species). Reuses the
existing corpus2.json + questions2.json. Writes data/r3_corpus2.json.

Run with that model's server up on :2235:
  python3 r3_corpus2.py <config>     e.g. lfm2 | granite | qwen06b_np
"""
import json
import os
import sys

import numpy as np

import chunkers
import lib
from lib import DATA, EMBEDDERS, SERVER_OF, Embedder, load_corpus, segment_corpus
from r3_build import GATE_PCTILES

C2 = os.path.join(DATA, "corpus2.json")
QS2 = os.path.join(DATA, "questions2.json")
OUT = os.path.join(DATA, "r3_corpus2.json")


def _acceptable(sents, M, qs):
    import re
    W = re.compile(r"[a-z0-9]+")
    cw = [set(w for w in W.findall(s.text.lower()) if len(w) > 2) for s in sents]
    acc = {}
    for q in qs:
        sid = q["support_sent"]; sims = M @ M[sid]
        a = {sid}
        for j in np.where(sims >= 0.90)[0]:
            j = int(j)
            if j != sid and cw[sid] and cw[j] and len(cw[sid] & cw[j]) / len(cw[sid] | cw[j]) >= 0.5:
                a.add(j)
        acc[q["id"]] = a
    return acc


def _families(sents, M):
    v = {"per_sentence": chunkers.per_sentence(sents)}
    for n in (2, 3, 5, 8, 13):
        v[f"fixed_n{n:02d}"] = chunkers.fixed_nsent(sents, n)
        nch = max(1, round(len(sents) / n))
        v[f"random_n{n:02d}"] = chunkers.random_cuts(sents, nch, seed=0)
    drift = 1.0 - np.sum(M[:-1] * M[1:], axis=1)
    for g in sorted({round(float(np.percentile(drift, p)), 4) for p in GATE_PCTILES}):
        v[f"semadj_g{g:.4f}"] = chunkers.semantic_adjacent(sents, M, gate=g)
    return v


def run(config):
    lib.GTOK_MODEL = SERVER_OF[config]
    lib.TOK = lib.gtok
    sents = segment_corpus(load_corpus(C2))
    emb = Embedder(config, verbose=False)
    M = emb.embed([s.text for s in sents], role="doc")
    M = M / (np.linalg.norm(M, axis=1, keepdims=True) + 1e-9)
    qs = json.load(open(QS2))
    acc = _acceptable(sents, M, qs)
    QV = emb.embed([q["q"] for q in qs], role="query")
    QV = QV / (np.linalg.norm(QV, axis=1, keepdims=True) + 1e-9)
    rows = []
    for name, chunks in _families(sents, M).items():
        cvec = emb.embed([c["text"] for c in chunks], role="doc")
        cvec = cvec / (np.linalg.norm(cvec, axis=1, keepdims=True) + 1e-9)
        spans = [(c["sent_start"], c["sent_end"]) for c in chunks]
        r1 = r5 = 0
        for qi, q in enumerate(qs):
            order = np.argsort(-(cvec @ QV[qi]))[:5]
            a = acc[q["id"]]
            hit = None
            for rank, ci in enumerate(order, 1):
                s0, s1 = spans[ci]
                if any(s0 <= x <= s1 for x in a):
                    hit = rank; break
            r1 += hit == 1
            r5 += bool(hit)
        fam = ("semadj" if name.startswith("semadj") else "fixed" if name.startswith("fixed")
               else "random" if name.startswith("random") else "persent")
        rows.append(dict(variant=name, family=fam,
                         tok_mean=float(np.mean([c["n_tok"] for c in chunks])),
                         n_chunks=len(chunks), recall1=r1 / len(qs), recall5=r5 / len(qs)))
        print(f"{name:18} R@1={r1/len(qs):.3f} R@5={r5/len(qs):.3f}", file=sys.stderr)
    json.dump({"model": EMBEDDERS[config]["model"], "config": config,
               "n_questions": len(qs), "results": rows}, open(OUT, "w"), indent=1)
    print("wrote", OUT, file=sys.stderr)


if __name__ == "__main__":
    run(sys.argv[1] if len(sys.argv) > 1 else "granite")
