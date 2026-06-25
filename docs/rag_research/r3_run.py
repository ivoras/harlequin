#!/usr/bin/env python3
"""
Orchestrate the whole single-embedder study. For each physical model in turn:
start its llama-server on :2235, benchmark raw embedding throughput, build all
index configs that model backs, evaluate all eval-configs that read them, then
stop the server before moving to the next model. Build/eval run as subprocesses
so per-model global state (token cache namespace, loaded questions) is isolated.

Usage:
  python3 r3_run.py            # all five models
  python3 r3_run.py gemma lfm2 # just these servers
"""
import json
import os
import subprocess
import sys
import time

import numpy as np
import requests

import r3_server
from lib import DATA, EMBEDDERS, load_corpus, segment_corpus
import lib

HERE = os.path.dirname(os.path.abspath(__file__))
PY = sys.executable

# which index-configs to BUILD and which eval-configs to RUN, per server backend.
BUILD_OF = {
    "granite": ["granite"],
    "snowflake": ["snowflake"],            # native doc-prefix empty: np reuses it
    "qwen06b": ["qwen06b"],
    "gemma": ["gemma", "gemma_np"],        # doc prefix differs -> two indexes
    "lfm2": ["lfm2", "lfm2_np"],
}
EVAL_OF = {
    "granite": ["granite"],
    "snowflake": ["snowflake", "snowflake_np"],
    "qwen06b": ["qwen06b", "qwen06b_np"],
    "gemma": ["gemma", "gemma_np"],
    "lfm2": ["lfm2", "lfm2_np"],
}


def bench(backend: str) -> dict:
    """Raw embedding throughput: re-embed the corpus sentences directly (cache
    bypassed), batched, timing server wall-clock and counting model tokens."""
    cfg = EMBEDDERS[backend]
    sents = [s.text for s in segment_corpus(load_corpus())]
    url, model = cfg["url"], cfg["model"]
    t0, n_tok, n_vec = time.time(), 0, 0
    for b in range(0, len(sents), 64):
        batch = sents[b:b + 64]
        r = requests.post(url, json={"model": model, "input": batch}, timeout=300)
        r.raise_for_status()
        d = r.json()
        n_tok += d.get("usage", {}).get("total_tokens", 0)
        n_vec += len(d["data"])
    dt = time.time() - t0
    return {"backend": backend, "dim": cfg["dim"], "n_vec": n_vec, "seconds": dt,
            "vec_per_sec": n_vec / dt, "tok_per_sec": (n_tok / dt) if n_tok else None,
            "tokens": n_tok}


def run(servers):
    bench_all = {}
    bp = os.path.join(DATA, "r3_bench.json")
    if os.path.exists(bp):
        bench_all = json.load(open(bp))
    for backend in servers:
        print(f"\n==== server: {backend} ====", file=sys.stderr)
        p = r3_server.start(backend)
        try:
            bench_all[backend] = bench(backend)
            json.dump(bench_all, open(bp, "w"), indent=1)
            print(f"  throughput {bench_all[backend]['vec_per_sec']:.1f} vec/s "
                  f"dim={bench_all[backend]['dim']}", file=sys.stderr)
            for cfg in BUILD_OF[backend]:
                print(f"  build {cfg}", file=sys.stderr)
                subprocess.run([PY, os.path.join(HERE, "r3_build.py"), cfg], check=True)
            for cfg in EVAL_OF[backend]:
                print(f"  eval {cfg}", file=sys.stderr)
                subprocess.run([PY, os.path.join(HERE, "r3_eval.py"), cfg], check=True)
        finally:
            r3_server.stop(p)
            time.sleep(1.0)
    print("\nall done", file=sys.stderr)


if __name__ == "__main__":
    servers = sys.argv[1:] or lib.MODELS
    run(servers)
