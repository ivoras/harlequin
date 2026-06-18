#!/usr/bin/env python3
"""
Generalization test on a contrasting corpus (Darwin, Origin of Species).
Replicates the two key findings — (1) recall tracks chunk size, (2) semantic
boundaries beat random ones at matched size — on non-legal, non-repetitive prose.

  python3 corpus2_run.py questions   # generate grounded questions (needs chat)
  python3 corpus2_run.py eval        # build chunk families + score (granite only)

Uses brute-force cosine (numpy); no vec0 needed at this scale.
"""
import json
import os
import random
import sys

import numpy as np

import chunkers
from lib import DATA, ChatLLM, Embedder, load_corpus, segment_corpus
import lib

C2 = os.path.join(DATA, "corpus2.json")
QS2 = os.path.join(DATA, "questions2.json")
RES2 = os.path.join(DATA, "r2_results_corpus2.json")
N_Q = 120


def _sents():
    lib.TOK = lib.gtok
    return segment_corpus(load_corpus(C2))


def gen_questions():
    sents = _sents()
    rng = random.Random(7)
    # candidate body sentences: informative, mid-length, skip front matter
    cand = [s for s in sents if s.id > 200 and 60 <= len(s.text) <= 300
            and sum(c.isalpha() for c in s.text) > 40]
    rng.shuffle(cand)
    llm = ChatLLM()
    out = []
    for s in cand:
        if len(out) >= N_Q:
            break
        q = llm.ask("Below is a sentence from a 19th-century book on biology. Write one "
                    "natural question a reader might ask whose answer is given by this "
                    "sentence. Output only the question.\n\nSentence: " + s.text,
                    max_tokens=60)
        if q and q.endswith("?") and len(q) > 10:
            out.append({"id": len(out), "q": q, "support_sent": s.id,
                        "page": s.page, "line": s.line, "expected_text": s.text[:200]})
    json.dump(out, open(QS2, "w"), ensure_ascii=False, indent=1)
    print(f"generated {len(out)} grounded questions for corpus2")


def _acceptable(sents, M):
    """duplicate-aware acceptable sets (cosine>=0.90 + lexical overlap)."""
    import re
    W = re.compile(r"[a-z0-9]+")
    cw = [set(w for w in W.findall(s.text.lower()) if len(w) > 2) for s in sents]
    acc = {}
    qs = json.load(open(QS2))
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
    for g in (0.10, 0.14, 0.18, 0.22, 0.26):
        v[f"semadj_g{g:.2f}"] = chunkers.semantic_adjacent(sents, M, gate=g)
    return v


def run_eval():
    sents = _sents()
    emb = Embedder("granite", verbose=False)
    M = emb.embed([s.text for s in sents], role="doc")
    M = M / (np.linalg.norm(M, axis=1, keepdims=True) + 1e-9)
    acc = _acceptable(sents, M)
    qs = json.load(open(QS2))
    QV = emb.embed([q["q"] for q in qs], role="query")
    QV = QV / (np.linalg.norm(QV, axis=1, keepdims=True) + 1e-9)
    fams = _families(sents, M)
    rows = []
    for name, chunks in fams.items():
        cvec = emb.embed([c["text"] for c in chunks], role="doc")
        cvec = cvec / (np.linalg.norm(cvec, axis=1, keepdims=True) + 1e-9)
        spans = [(c["sent_start"], c["sent_end"]) for c in chunks]
        r1 = r5 = 0
        for qi, q in enumerate(qs):
            sims = cvec @ QV[qi]
            order = np.argsort(-sims)[:5]
            a = acc[q["id"]]
            hit_rank = None
            for rank, ci in enumerate(order, 1):
                s0, s1 = spans[ci]
                if any(s0 <= x <= s1 for x in a):
                    hit_rank = rank; break
            if hit_rank == 1:
                r1 += 1
            if hit_rank:
                r5 += 1
        tok = float(np.mean([c["n_tok"] for c in chunks]))
        fam = ("semadj" if name.startswith("semadj") else "fixed" if name.startswith("fixed")
               else "random" if name.startswith("random") else "persent")
        rows.append(dict(variant=name, family=fam, tok_mean=tok, n_chunks=len(chunks),
                         recall1=r1/len(qs), recall5=r5/len(qs)))
        print(f"{name:16} tok={tok:6.0f} R@1={r1/len(qs):.3f} R@5={r5/len(qs):.3f}")
    json.dump({"n_questions": len(qs), "results": rows}, open(RES2, "w"), indent=1)
    print("wrote", RES2)


if __name__ == "__main__":
    {"questions": gen_questions, "eval": run_eval}[sys.argv[1]]()
