#!/usr/bin/env python3
"""
Minimal BM25 (Okapi) over a fixed set of chunk texts. Pure numpy; no external
deps. Used for the lexical arm of hybrid retrieval — important for legal text
where exact tokens (article numbers, "qualified majority", "Article 50") matter.
"""
import math
import re
from collections import Counter

import numpy as np

_WORD = re.compile(r"[a-z0-9]+")
# light stopword list; keep numbers and legal terms
_STOP = set("the a an of to in and or for on by with as at is are be shall this that "
            "which it its from not no any all may than have has".split())


def tokenize(text: str) -> list[str]:
    return [w for w in _WORD.findall(text.lower()) if w not in _STOP and len(w) > 1]


class BM25:
    def __init__(self, docs: list[str], k1: float = 1.5, b: float = 0.75):
        self.k1, self.b = k1, b
        self.toks = [tokenize(d) for d in docs]
        self.N = len(docs)
        self.dl = np.array([len(t) for t in self.toks], dtype=np.float32)
        self.avgdl = float(self.dl.mean()) if self.N else 0.0
        df = Counter()
        for t in self.toks:
            df.update(set(t))
        # idf with the standard BM25 +0.5 smoothing, floored at 0
        self.idf = {w: max(math.log((self.N - n + 0.5) / (n + 0.5) + 1.0), 0.0)
                    for w, n in df.items()}
        self.tf = [Counter(t) for t in self.toks]

    def scores(self, query: str) -> np.ndarray:
        q = tokenize(query)
        s = np.zeros(self.N, dtype=np.float32)
        for w in q:
            idf = self.idf.get(w)
            if not idf:
                continue
            for i in range(self.N):
                f = self.tf[i].get(w, 0)
                if f:
                    denom = f + self.k1 * (1 - self.b + self.b * self.dl[i] / (self.avgdl + 1e-9))
                    s[i] += idf * f * (self.k1 + 1) / (denom + 1e-9)
        return s

    def topk(self, query: str, k: int = 10) -> list[tuple[int, float]]:
        s = self.scores(query)
        idx = np.argsort(-s)[:k]
        return [(int(i), float(s[i])) for i in idx if s[i] > 0]
