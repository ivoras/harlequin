#!/usr/bin/env python3
"""
Chunking strategies. Each chunker takes the global list of Sentence objects
and returns a list of chunk dicts. Every chunk is a *contiguous run of
sentences*, so it carries sent_start/sent_end (global sentence ids) which the
evaluator uses to score retrieval precisely.

Strategies
----------
mechanical(max_tok)        Greedy sentence packing up to a token budget; never
                           splits a sentence. (the baseline)
overlapping(max_tok)       Mechanical, but each chunk also carries the last
                           sentence of the previous chunk and the first
                           sentence of the next one (1-sentence overlap each
                           side). Overlap on a side is skipped if it would push
                           the chunk over the token budget.
successive(gate, max_tok)  Semantic-drift chunking. Grow a chunk one sentence
                           at a time, embedding the cumulative text each step.
                           When the cumulative embedding drifts from the
                           previous step by more than `gate`
                           (drift = 1 - cosine(prev, curr)), cut before the
                           sentence that caused the drift. A hard token cap
                           (model context) also forces a cut.
structure(max_tok)         Domain-aware: start a new chunk at every "Article N"
                           / "TITLE" / "CHAPTER" heading, then pack to budget.
per_sentence()             One chunk per sentence (fine-grained retrieval).

NOTE on `successive` and the gate
---------------------------------
The brief said "add sentences ... until the similarity of vectors from two
successive runs falls below 0.1" and asked to sweep gates 0.05..0.25. Taken at
face value (cut when cosine < gate) the cut never fires: successive cumulative
embeddings of normalized vectors stay >0.7 similar, so all gates collapse to
the token cap. To make the parameter sweep meaningful we operationalize the
gate as a *drift tolerance*: cut when drift = (1 - cosine) exceeds the gate.
Small gate -> sensitive -> small chunks; large gate -> tolerant -> big chunks.
This is documented in the report.
"""
from __future__ import annotations

import lib
from lib import Sentence, Embedder, cosine
import numpy as np


def _mk_chunk(sents: list[Sentence], sent_ids: list[int]) -> dict:
    chosen = [sents[i] for i in sent_ids]
    text = " ".join(s.text for s in chosen)
    pages = sorted({s.page for s in chosen})
    return {
        "text": text,
        "first_page": chosen[0].page,
        "first_line": chosen[0].line,
        "last_page": chosen[-1].page,
        "last_line": chosen[-1].line,
        "sent_start": sent_ids[0],
        "sent_end": sent_ids[-1],
        "pages": pages,
        # budget is the sum of per-sentence token counts; the realised chunk
        # token count is <= that sum (BPE does not merge across spaces), so a
        # cap on the budget keeps every chunk within the model's limit.
        "n_tok": sum(s.n_tok for s in chosen),
    }


def mechanical(sents: list[Sentence], max_tok: int = 1024) -> list[dict]:
    chunks, cur, cur_tok = [], [], 0
    for s in sents:
        st = s.n_tok
        if cur and cur_tok + st > max_tok:
            chunks.append(_mk_chunk(sents, cur))
            cur, cur_tok = [], 0
        cur.append(s.id)
        cur_tok += st
    if cur:
        chunks.append(_mk_chunk(sents, cur))
    return chunks


def overlapping(sents: list[Sentence], max_tok: int = 1024) -> list[dict]:
    """1-sentence overlap each side; skip a side's overlap if over budget."""
    base = mechanical(sents, max_tok)
    out = []
    for c in base:
        ids = list(range(c["sent_start"], c["sent_end"] + 1))
        tok = c["n_tok"]
        # try left overlap
        if ids[0] > 0:
            prev = sents[ids[0] - 1]
            if tok + prev.n_tok <= max_tok:
                ids = [prev.id] + ids
                tok += prev.n_tok
        # try right overlap
        if ids[-1] < len(sents) - 1:
            nxt = sents[ids[-1] + 1]
            if tok + nxt.n_tok <= max_tok:
                ids = ids + [nxt.id]
                tok += nxt.n_tok
        out.append(_mk_chunk(sents, ids))
    return out


def successive(sents: list[Sentence], embedder: Embedder, gate: float = 0.1,
               max_tok: int = 1024) -> list[dict]:
    """
    Semantic-drift chunking. Embeds cumulative chunk text incrementally.
    Cut when drift = 1 - cos(prev_cumulative, curr_cumulative) > gate, or when
    the token cap would be exceeded. Uses the embedding cache so repeated runs
    across gates are cheap.
    """
    chunks: list[dict] = []
    cur: list[int] = []
    cur_tok = 0
    prev_vec: np.ndarray | None = None

    def flush():
        nonlocal cur, cur_tok, prev_vec
        if cur:
            chunks.append(_mk_chunk(sents, cur))
        cur, cur_tok, prev_vec = [], 0, None

    for s in sents:
        # token cap -> hard cut
        if cur and cur_tok + s.n_tok > max_tok:
            flush()
        if not cur:
            cur = [s.id]
            cur_tok = s.n_tok
            prev_vec = embedder.embed_one(" ".join(sents[i].text for i in cur))
            continue
        trial = cur + [s.id]
        trial_text = " ".join(sents[i].text for i in trial)
        trial_vec = embedder.embed_one(trial_text)
        drift = 1.0 - cosine(prev_vec, trial_vec)
        if drift > gate:
            # this sentence shifts meaning too much: cut before it
            flush()
            cur = [s.id]
            cur_tok = s.n_tok
            prev_vec = embedder.embed_one(s.text)
        else:
            cur = trial
            cur_tok += s.n_tok
            prev_vec = trial_vec
    flush()
    return chunks


def semantic_adjacent(sents: list[Sentence], M: np.ndarray, gate: float = 0.15,
                      max_tok: int = 1024, min_sent: int = 2) -> list[dict]:
    """
    Improved 'successive' chunking. Instead of re-embedding the cumulative text
    (whose mean-pooled vector barely moves once the chunk is large), cut between
    sentence i and i+1 when their *adjacent* embedding distance exceeds the gate:
        drift = 1 - cos(emb[i], emb[i+1])
    A topic shift between consecutive sentences is a real, well-scaled signal
    (drift ~ 0.0-0.24 on this corpus), so the gate actually bites. A token cap
    still guards the model limit, and `min_sent` avoids singleton fragments.
    M is the L2-normalised per-sentence embedding matrix (cheap, one batched pass).
    """
    chunks, cur, cur_tok = [], [], 0
    for i, s in enumerate(sents):
        if cur:
            over_cap = cur_tok + s.n_tok > max_tok
            drift = 1.0 - float(np.dot(M[cur[-1]], M[s.id]))
            big_shift = drift > gate and len(cur) >= min_sent
            if over_cap or big_shift:
                chunks.append(_mk_chunk(sents, cur))
                cur, cur_tok = [], 0
        cur.append(s.id)
        cur_tok += s.n_tok
    if cur:
        chunks.append(_mk_chunk(sents, cur))
    return chunks


def semantic_centroid(sents: list[Sentence], M: np.ndarray, gate: float = 0.15,
                      max_tok: int = 1024, min_sent: int = 2) -> list[dict]:
    """
    Variant that compares each new sentence to the running *centroid* of the
    current chunk (its mean direction), not just its immediate neighbour:
        drift = 1 - cos(emb[i], normalise(mean(chunk embeddings)))
    More robust than adjacent-only: a single stylistic outlier sentence does not
    force a cut, but a sustained topic change does.
    """
    chunks, cur, cur_tok = [], [], 0
    csum = np.zeros(M.shape[1], dtype=np.float64)
    for s in sents:
        if cur:
            centroid = csum / len(cur)
            centroid = centroid / (np.linalg.norm(centroid) + 1e-9)
            drift = 1.0 - float(np.dot(centroid, M[s.id]))
            if (cur_tok + s.n_tok > max_tok) or (drift > gate and len(cur) >= min_sent):
                chunks.append(_mk_chunk(sents, cur))
                cur, cur_tok, csum = [], 0, np.zeros(M.shape[1], dtype=np.float64)
        cur.append(s.id)
        cur_tok += s.n_tok
        csum += M[s.id]
    if cur:
        chunks.append(_mk_chunk(sents, cur))
    return chunks


_HEAD_PREFIXES = ("Article ", "TITLE ", "CHAPTER ", "SECTION ", "PART ", "PROTOCOL", "ANNEX")


def _is_heading(text: str) -> bool:
    t = text.strip()
    return any(t.startswith(p) for p in _HEAD_PREFIXES)


def structure(sents: list[Sentence], max_tok: int = 1024) -> list[dict]:
    """Start a new chunk at structural headings, then pack to the budget."""
    chunks, cur, cur_tok = [], [], 0
    for s in sents:
        start_new = bool(cur) and (
            _is_heading(s.text) or cur_tok + s.n_tok > max_tok
        )
        if start_new:
            chunks.append(_mk_chunk(sents, cur))
            cur, cur_tok = [], 0
        cur.append(s.id)
        cur_tok += s.n_tok
    if cur:
        chunks.append(_mk_chunk(sents, cur))
    return chunks


def per_sentence(sents: list[Sentence]) -> list[dict]:
    return [_mk_chunk(sents, [s.id]) for s in sents]
