#!/usr/bin/env python3
"""
llama-server lifecycle manager for the single-embedder study.

Only ONE embedding model is ever resident: each is served in turn on :2235
(matching ~/LLM/embeddings_server.sh), so every result is produced with that one
model doing boundaries + chunk vectors + query vectors. start() launches the
server, waits for /health, and detects the embedding dimension from a probe;
stop() tears it down. Used as a context manager:

    with serve("granite") as info:
        ... build / eval against http://localhost:2235 ...
"""
import contextlib
import os
import signal
import subprocess
import sys
import time

import requests

LLM_DIR = os.path.expanduser("~/LLM")
PORT = 2235
BASE = f"http://localhost:{PORT}"

# model gguf + context window per backend. All served on :2235, one at a time.
SERVERS = {
    "granite":   dict(gguf="granite-embedding-311M-multilingual-r2-Q8_0.gguf", ctx=2500),
    "snowflake": dict(gguf="snowflake-arctic-embed-l-v2.0-q8_0.gguf",          ctx=2500),
    "qwen06b":   dict(gguf="Qwen3-Embedding-0.6B-Q8_0.gguf",                   ctx=2500),
    "gemma":     dict(gguf="embeddinggemma-300M-Q8_0.gguf",                    ctx=2000),
    "lfm2":      dict(gguf="LFM2.5-Embedding-350M-Q8_0.gguf",                  ctx=2500),
}


def _health_ok() -> bool:
    try:
        r = requests.get(BASE + "/health", timeout=2)
        return r.status_code == 200
    except Exception:  # noqa: BLE001
        return False


def probe_dim(model: str) -> int:
    r = requests.post(BASE + "/v1/embeddings",
                      json={"model": model, "input": ["dimension probe"]}, timeout=60)
    r.raise_for_status()
    return len(r.json()["data"][0]["embedding"])


def start(backend: str, log_path: str | None = None) -> subprocess.Popen:
    cfg = SERVERS[backend]
    ctx = cfg["ctx"]
    log = open(log_path or os.path.join(LLM_DIR, f"emb_{backend}.log"), "w")
    cmd = ["llama-server", "-m", cfg["gguf"], "--embeddings",
           "-c", str(ctx), "-ub", str(ctx), "-b", str(ctx),
           "-ngl", "99", "--port", str(PORT), "--host", "0.0.0.0", "-cram", "0"]
    if _health_ok():
        raise RuntimeError(f"port {PORT} already serving; stop it first")
    p = subprocess.Popen(cmd, cwd=LLM_DIR, stdout=log, stderr=subprocess.STDOUT,
                         preexec_fn=os.setsid)
    for _ in range(180):
        if p.poll() is not None:
            raise RuntimeError(f"llama-server exited early ({p.returncode}); see log")
        if _health_ok():
            return p
        time.sleep(1.0)
    stop(p)
    raise RuntimeError("server did not become healthy within 180s")


def stop(p: subprocess.Popen):
    if p.poll() is not None:
        return
    with contextlib.suppress(Exception):
        os.killpg(os.getpgid(p.pid), signal.SIGTERM)
    for _ in range(30):
        if p.poll() is not None:
            return
        time.sleep(0.5)
    with contextlib.suppress(Exception):
        os.killpg(os.getpgid(p.pid), signal.SIGKILL)


@contextlib.contextmanager
def serve(backend: str):
    p = start(backend)
    try:
        yield p
    finally:
        stop(p)


if __name__ == "__main__":
    # probe: bring each model up, report dim + ctx, tear down.
    which = sys.argv[1:] or list(SERVERS)
    for b in which:
        p = start(b)
        try:
            d = probe_dim(SERVERS[b]["gguf"])
            print(f"{b:10} dim={d:5} gguf={SERVERS[b]['gguf']}")
        finally:
            stop(p)
        time.sleep(1.0)
