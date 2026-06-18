#!/usr/bin/env python3
"""
Build duplicate-aware acceptable-answer sets. The TEU repeats clauses verbatim
(e.g. "acting by a qualified majority", the UK/Denmark opt-out boilerplate), so
crediting only the one authored support sentence under-counts correct retrievals.

For each in-document question we accept its support sentence plus any sentence
whose granite embedding is >= TAU cosine to it AND shares substantial lexical
overlap (Jaccard over content words) — the lexical guard prevents over-accepting
generic high-similarity sentences. Output: data/answer_sets.json
  { "<qid>": [sent_id, ...], ... }
"""
import json
import os
import re

import numpy as np

from lib import DATA, Embedder, load_corpus, segment_corpus

TAU = 0.90
JACCARD = 0.5
_W = re.compile(r"[a-z0-9]+")


def content_words(t):
    return set(w for w in _W.findall(t.lower()) if len(w) > 2)


def main():
    sents = segment_corpus(load_corpus())
    texts = [s.text for s in sents]
    cw = [content_words(t) for t in texts]
    e = Embedder("granite", verbose=False)
    M = e.embed(texts, role="doc")
    M = M / (np.linalg.norm(M, axis=1, keepdims=True) + 1e-9)

    qs = json.load(open(os.path.join(DATA, "eval_questions.json")))["questions"]
    out, sizes = {}, []
    for q in qs:
        if q["not_found"]:
            continue
        sid = q["support_sent"]
        sims = M @ M[sid]
        cand = np.where(sims >= TAU)[0]
        acc = {sid}
        for j in cand:
            j = int(j)
            if j == sid:
                continue
            a, b = cw[sid], cw[j]
            if a and b and len(a & b) / len(a | b) >= JACCARD:
                acc.add(j)
        out[str(q["id"])] = sorted(acc)
        sizes.append(len(acc))
    json.dump(out, open(os.path.join(DATA, "answer_sets.json"), "w"))
    sizes = np.array(sizes)
    print(f"answer sets: {len(out)} questions")
    print(f"  acceptable/question: mean={sizes.mean():.2f} median={int(np.median(sizes))} "
          f"max={sizes.max()}  with>1: {(sizes>1).mean()*100:.0f}%")


if __name__ == "__main__":
    main()
