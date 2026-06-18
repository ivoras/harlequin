#!/usr/bin/env python3
"""
Unified retriever over one index: dense (sqlite-vec), lexical (BM25), and their
RRF hybrid. Query vectors are supplied precomputed so the evaluator can batch
all embeddings once per backend.
"""
import os
import sqlite3

import numpy as np

from bm25 import BM25, tokenize
from lib import DATA, Embedder, VectorStore

IDX = os.path.join(DATA, "idx")          # data/idx/{backend}/{variant}.sqlite


def idx_path(backend: str, variant: str) -> str:
    return os.path.join(IDX, backend, f"{variant}.sqlite")


def rrf(*ranked_lists, k: int = 10, c: int = 60) -> list[tuple[int, float]]:
    """Reciprocal Rank Fusion of (id, score) lists -> fused (id, rrf_score)."""
    agg: dict[int, float] = {}
    for lst in ranked_lists:
        for rank, (cid, _s) in enumerate(lst, start=1):
            agg[cid] = agg.get(cid, 0.0) + 1.0 / (c + rank)
    out = sorted(agg.items(), key=lambda x: -x[1])
    return out[:k]


class Retriever:
    def __init__(self, backend: str, variant: str, embedder: Embedder | None = None,
                 with_bm25: bool = True, with_fts: bool = False):
        self.backend = backend
        self.variant = variant
        self.emb = embedder or Embedder(backend, verbose=False)
        self.store = VectorStore(idx_path(backend, variant), dim=self.emb.dim)
        self.chunks = self.store.all_chunks()           # ordered by id
        self.ids = [c["id"] for c in self.chunks]
        self.by_id = {c["id"]: c for c in self.chunks}
        self.bm25 = BM25([c["text"] for c in self.chunks]) if with_bm25 else None
        self.fts = None
        if with_fts:
            self.fts = sqlite3.connect(":memory:")
            self.fts.execute("CREATE VIRTUAL TABLE fts USING fts5(text)")
            self.fts.executemany("INSERT INTO fts(rowid, text) VALUES (?, ?)",
                                 [(c["id"], c["text"]) for c in self.chunks])

    # --- single-arm retrievers ------------------------------------------------
    def dense(self, qvec: np.ndarray, k: int = 10) -> list[tuple[int, float]]:
        return self.store.search(qvec, k)

    def lexical(self, qtext: str, k: int = 10) -> list[tuple[int, float]]:
        return [(self.ids[i], s) for i, s in self.bm25.topk(qtext, k)]

    def fts5(self, qtext: str, k: int = 10) -> list[tuple[int, float]]:
        """SQLite FTS5 lexical search ranked by its built-in BM25. Returns
        (id, score) with score = -bm25 so higher is better, like the other arms."""
        terms = tokenize(qtext)
        if not terms:
            return []
        match = " OR ".join('"%s"' % t.replace('"', "") for t in terms)
        rows = self.fts.execute(
            "SELECT rowid, bm25(fts) FROM fts WHERE fts MATCH ? ORDER BY bm25(fts) LIMIT ?",
            (match, k)).fetchall()
        return [(int(rid), -float(b)) for rid, b in rows]

    def hybrid(self, qvec: np.ndarray, qtext: str, k: int = 10,
               depth: int = 50, c: int = 60) -> list[tuple[int, float]]:
        d = self.dense(qvec, depth)
        l = self.lexical(qtext, depth)
        return rrf(d, l, k=k, c=c)

    def hybrid_fts(self, qvec: np.ndarray, qtext: str, k: int = 10,
                   depth: int = 50, c: int = 60) -> list[tuple[int, float]]:
        return rrf(self.dense(qvec, depth), self.fts5(qtext, depth), k=k, c=c)
