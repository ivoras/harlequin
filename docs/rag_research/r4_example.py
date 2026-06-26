#!/usr/bin/env python3
"""
Worked example for paper §6.1: how the sem_adjacent chunker bundles sentences.

Uses the real (cached) snowflake per-sentence embeddings of the TEU corpus,
computes the adjacent cosine distance between consecutive sentences, and applies
the sem_adjacent rule at snowflake's selected gate to show where it cuts. Picks
an illustrative window automatically (a readable run that produces a couple of
chunks with both bundled and cut boundaries). Offline (cache only). Writes
data/r4_semadj_example.json.
"""
import json
import os

import numpy as np

import lib
from lib import DATA, Embedder, load_corpus, segment_corpus

GATE_DEFAULT = 0.3772
MIN_SENT = 2
WIN = 8          # sentences shown in the example


def best_gate():
    p = os.path.join(DATA, "r4_agg.json")
    if os.path.exists(p):
        g = json.load(open(p)).get("gate", {}).get("snowflake", {}).get("best_gate")
        if g:
            return float(g)
    return GATE_DEFAULT


def semadj_window(ids, M, gate):
    """Run the sem_adjacent rule over a window; return cut-after flags + chunks."""
    chunks, cur, cut_after = [], [], {}
    for k, i in enumerate(ids):
        if cur:
            drift = 1.0 - float(np.dot(M[cur[-1]], M[i]))
            if drift > gate and len(cur) >= MIN_SENT:
                chunks.append(cur)
                cut_after[cur[-1]] = True
                cur = []
        cur.append(i)
    if cur:
        chunks.append(cur)
    return chunks, cut_after


def main():
    gate = best_gate()
    sents = segment_corpus(load_corpus())
    emb = Embedder("snowflake", verbose=False)
    M = emb.embed([s.text for s in sents], role="doc")   # cached
    M = M / (np.linalg.norm(M, axis=1, keepdims=True) + 1e-9)
    drift = 1.0 - np.sum(M[:-1] * M[1:], axis=1)

    # pick a readable window that shows BOTH behaviours: a genuine low-drift
    # bundle (a chunk of >=3 sentences kept together because they stay similar)
    # and at least one cut. Short-ish sentences, skipping front matter.
    pick = None
    for start in range(250, len(sents) - WIN):
        ids = list(range(start, start + WIN))
        if any(len(sents[i].text) > 170 or len(sents[i].text) < 25 for i in ids):
            continue
        chunks, cut_after = semadj_window(ids, M, gate)
        if len(chunks) >= 2 and max(len(c) for c in chunks) >= 3 \
                and min(float(drift[i]) for i in ids[:-1]) < gate:
            pick = (ids, chunks, cut_after)
            break
    if pick is None:                                  # fallback: any window with cuts
        start = 400
        ids = list(range(start, start + WIN))
        pick = (ids,) + semadj_window(ids, M, gate)
    ids, chunks, cut_after = pick

    out_sents = []
    for k, i in enumerate(ids):
        dn = float(drift[i]) if i < len(sents) - 1 and k < len(ids) - 1 else None
        out_sents.append({
            "id": i, "text": sents[i].text[:150] + ("…" if len(sents[i].text) > 150 else ""),
            "drift_next": dn, "cut_after": bool(cut_after.get(i, False))})
    out = {"gate": gate, "min_sent": MIN_SENT,
           "sentences": out_sents,
           "chunks": [{"ids": c, "n_sent": len(c),
                       "preview": " ".join(sents[i].text for i in c)[:200] + "…"} for c in chunks],
           "drift_stats": {"median": float(np.median(drift)),
                           "p25": float(np.percentile(drift, 25)),
                           "p75": float(np.percentile(drift, 75))}}
    json.dump(out, open(os.path.join(DATA, "r4_semadj_example.json"), "w"),
              ensure_ascii=False, indent=1)
    print(f"gate={gate:.4f}  window sents {ids[0]}..{ids[-1]}  -> {len(chunks)} chunks "
          f"{[len(c) for c in chunks]}")
    for s in out_sents:
        d = f"{s['drift_next']:.3f}" if s["drift_next"] is not None else "  -  "
        print(f"  s{s['id']} drift_next={d} {'CUT' if s['cut_after'] else '   '} | {s['text'][:70]}")


if __name__ == "__main__":
    main()
