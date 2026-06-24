#!/usr/bin/env python3
"""
Build the confound-curve JSON consumed by make_paper2's Figure 1 (recall@1 vs
chunk size, by boundary rule). Joins an r2_results_*confound*.json with each
index's stored tok_mean meta, and tags every variant with its boundary family.

Usage:
  python3 r2_confound_curve.py granite r2_results_confound.json       confound_curve.json
  python3 r2_confound_curve.py qwen06b r2_results_qwen_confound.json  confound_curve_qwen06b.json
"""
import json
import os
import sys

from lib import DATA, VectorStore
from r2_retrieval import idx_path


def family_of(variant: str) -> str:
    for fam in ("semadj", "semcen", "fixed", "random"):
        if variant.startswith(fam):
            return fam
    return "other"


def build(backend: str, results_file: str, out_file: str):
    res = json.load(open(os.path.join(DATA, results_file)))["results"]
    out = []
    for r in res:
        if r["backend"] != backend:
            continue
        v = r["variant"]
        meta = VectorStore(idx_path(backend, v)).get_meta()
        out.append({
            "family": family_of(v),
            "tok_mean": meta.get("tok_mean"),
            "variant": v,
            "recall1": r["recall@1"],
            "recall5": r["recall@5"],
            "ans1024": r["answer@1024tok"],
            "auc": r["ood_auc"],
        })
    out.sort(key=lambda x: (x["family"], x["tok_mean"]))
    json.dump(out, open(os.path.join(DATA, out_file), "w"), indent=1)
    print(f"wrote {out_file}: {len(out)} variants for {backend}", file=sys.stderr)


if __name__ == "__main__":
    backend = sys.argv[1] if len(sys.argv) > 1 else "granite"
    results_file = sys.argv[2] if len(sys.argv) > 2 else "r2_results_confound.json"
    out_file = sys.argv[3] if len(sys.argv) > 3 else "confound_curve.json"
    build(backend, results_file, out_file)
