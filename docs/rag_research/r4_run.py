#!/usr/bin/env python3
"""
v4 re-evaluation on the augmented (1152-question) eval set. Indexes are
corpus-derived and unchanged, so this skips build/bench and only re-runs the
evaluator per model: bring up each server in turn, embed the new queries, and
re-score every eval-config into r4_results_*.json (RPREFIX=r4).

Usage: python3 r4_run.py [servers…]
"""
import os
import subprocess
import sys
import time

import lib
import r3_server
from r3_run import EVAL_OF, HERE, PY


def run(servers):
    env = {**os.environ, "RPREFIX": "r4"}
    for backend in servers:
        print(f"\n==== server: {backend} ====", file=sys.stderr)
        p = r3_server.start(backend)
        try:
            for cfg in EVAL_OF[backend]:
                print(f"  eval {cfg}", file=sys.stderr)
                subprocess.run([PY, os.path.join(HERE, "r3_eval.py"), cfg],
                               check=True, env=env)
        finally:
            r3_server.stop(p)
            time.sleep(1.0)
    print("\nv4 eval done", file=sys.stderr)


if __name__ == "__main__":
    run(sys.argv[1:] or lib.MODELS)
