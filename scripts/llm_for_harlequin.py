#!/usr/bin/env python3
"""
llm_for_harlequin.py - launch and monitor the three llama.cpp servers Harlequin uses:

  * chat   - the big model that drives the conversation
  * aux    - a tiny, fast model for auxiliary delegate work (WebFetch / WebFetchDOM
             analysis, etc.); given a generous 24K context
  * embed  - the embedding model

It starts all three, prints an ASCII monitoring dashboard (status, RSS, request
and token counters scraped from each server's /metrics), and on Ctrl-C shuts all
of them down cleanly.

To change models / ports / flags, edit the SERVERS list below - that is the only
section you should normally need to touch.
"""

import os
import re
import shutil
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
from datetime import timedelta

# --------------------------------------------------------------------------- #
# Configuration - edit this block to change models, ports, or llama.cpp flags. #
# --------------------------------------------------------------------------- #

# Directory holding the .gguf files and where per-server logs are written.
MODEL_DIR = os.path.dirname(os.path.abspath(__file__))
LOG_DIR = os.path.join(MODEL_DIR, "logs")

# Path to the llama.cpp server binary (found on PATH by default).
LLAMA_SERVER = shutil.which("llama-server") or os.path.expanduser(
    "~/llama.cpp/build/bin/llama-server"
)

HOST = "0.0.0.0"           # bind address passed to every server
REFRESH_SECONDS = 2.0      # dashboard refresh interval

# llama-server log verbosity (-lv / --verbosity). Levels: 0 generic, 1 error,
# 2 warning, 3 info (default), 4 trace, 5 debug.
#
# Level 4 is REQUIRED for the dashboard's memory columns: llama-server only
# prints its model / KV-cache / compute buffer sizes (the actual VRAM/RAM the
# model occupies) at verbosity >= 4. Levels 0-3 print none of it; level 5 adds a
# no_alloc dry-run pass that misreports the model buffer as 0.00 MiB, plus a lot
# of per-token noise. So 4 is the value to use. The sizes are logged once at load
# and parsed from logs/<name>.log.
VERBOSITY = 4

# Each server: a human name, the model file (relative to MODEL_DIR), the port,
# and the full llama-server argument list (everything except -m/--port/--host,
# which are added automatically). "kind" only affects which columns are shown.
SERVERS = [
    {
        "name": "chat",
        "kind": "chat",
        "model": "Qwen3.6-35B-A3B-IQ4_XS-3.53bpw.gguf",
        "port": 2234,
        # Mirrors llm_for_hermes_meral.sh (the chat model launch script).
        "args": [
            "-c", "100000", "--timeout", "3600",
            "-ctk", "q8_0", "-ctv", "q8_0", "--kv-unified",
            "--batch-size", "4096", "-np", "2",
            "--presence-penalty", "0.5", "--repeat-penalty", "1.05",
            "--temperature", "0.5", "--min_p", "0.05", "--top_p", "0.95",
            "-cram", "6144",
            "--ctx-checkpoints", "64", "--checkpoint-min-step", "256",
            "--cache-reuse", "2",
            "--reasoning-budget", "3000",
            "--reasoning-budget-message", "Thinking budget exceeded, answer now",
            "--reasoning-preserve",
            "--spec-type", "ngram-map-k4v",
            "--spec-ngram-map-k4v-size-n", "8",
            "--spec-ngram-map-k4v-size-m", "8",
            "--spec-ngram-map-k4v-min-hits", "2",
            "--spec-draft-n-max", "64",
            "--swa-full",
        ],
    },
    {
        "name": "aux",
        "kind": "aux",
        "model": "qwen35-0.8b-spectralquant-calib360-q4_k_m.gguf",
        "port": 2236,
        # Tiny WebFetch/WebFetchDOM analysis model. 24K context. Same KV-cache and
        # batching style as the chat model; low temperature for consistent
        # extraction. Thinking is controlled per-request by the client
        # (enable_thinking=false), so no reasoning budget is forced here.
        "args": [
            "-c", "24576", "--timeout", "3600",
            "-ctk", "q8_0", "-ctv", "q8_0", "--kv-unified",
            "--batch-size", "4096", "-np", "2",
            "--temperature", "0.1", "--min_p", "0.05", "--top_p", "0.95",
            "--cache-reuse", "2",
            "-ngl", "99",
        ],
    },
    {
        "name": "embed",
        "kind": "embed",
        "model": "Qwen3-Embedding-0.6B-Q8_0.gguf",
        "port": 2235,
        # Mirrors embeddings_server.sh.
        "args": [
            "--embeddings",
            "-c", "2560", "-ub", "2560", "-b", "2560",
            "-ngl", "99", "-cram", "0",
        ],
    },
]

# --------------------------------------------------------------------------- #
# Implementation - you normally don't need to edit below here.                 #
# --------------------------------------------------------------------------- #

def build_command(srv):
    """Full llama-server argv for one server entry."""
    return [
        LLAMA_SERVER,
        "-m", os.path.join(MODEL_DIR, srv["model"]),
        "--host", HOST,
        "--port", str(srv["port"]),
        "--metrics",            # needed for the dashboard's token/request columns
        "-lv", str(VERBOSITY),  # >=4 makes llama-server log its buffer sizes (memory)
        *srv["args"],
    ]


def http_get(port, path, timeout=1.0):
    """GET http://127.0.0.1:<port><path>; return (status_code, body) or (None, '')."""
    url = "http://127.0.0.1:%d%s" % (port, path)
    try:
        with urllib.request.urlopen(url, timeout=timeout) as r:
            return r.status, r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, ""
    except Exception:
        return None, ""


def parse_metrics(body):
    """Parse llama.cpp Prometheus /metrics text into {metric_name: float}."""
    out = {}
    for line in body.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split()
        if len(parts) >= 2:
            try:
                out[parts[0]] = float(parts[1])
            except ValueError:
                pass
    return out


# Memory parsing: llama-server logs the actual buffer sizes it allocates, but only
# at verbosity >= 4 (see VERBOSITY). We scrape those one-time load lines from each
# server's log. Examples of the lines matched:
#   load_tensors:      Vulkan0 model buffer size =   522.43 MiB
#   llama_kv_cache:    Vulkan0 KV buffer size =    48.00 MiB
#   llama_memory_recurrent:  Vulkan0 RS buffer size =   77.06 MiB
#   sched_reserve:     Vulkan0 compute buffer size =    48.30 MiB
#   llama_context: Vulkan_Host  output buffer size =     3.79 MiB
#   print_info: file size   = 522.43 MiB (5.82 BPW)
_BUF_RE = re.compile(r"(model|KV|RS|compute|output) buffer size\s*=\s*([\d.]+)\s*MiB")
_FILESIZE_RE = re.compile(r"print_info: file size\s*=\s*([\d.]+)\s*MiB")
_DEV_RE = re.compile(r"-\s*(Vulkan\d+|ROCm\d+|CUDA\d+)\s*:.*\((\d+)\s*MiB,\s*(\d+)\s*MiB free\)")

# Parsed memory is static (logged once at load), so cache it per server name.
_mem_cache = {}


def parse_memory(name, path):
    """Scrape per-server memory (MiB) from its llama-server log. Returns a dict
    with model/state/compute/total and the file size + GPU device line, or {} if
    the buffer-size lines aren't present yet (verbosity < 4, or still loading)."""
    if name in _mem_cache:
        return _mem_cache[name]
    try:
        with open(path, "r", errors="replace") as f:
            text = f.read()
    except OSError:
        return {}

    model = state = compute = 0.0
    found = False
    for kind, val in _BUF_RE.findall(text):
        mib = float(val)
        if mib <= 0:
            continue  # skip no_alloc dry-run zeros (verbosity 5)
        found = True
        if kind == "model":
            model += mib
        elif kind in ("KV", "RS"):
            state += mib
        else:  # compute, output
            compute += mib
    if not found:
        return {}  # not logged yet -> don't cache, retry next refresh

    mem = {
        "model": model, "state": state, "compute": compute,
        "total": model + state + compute,
    }
    fs = _FILESIZE_RE.search(text)
    if fs:
        mem["file"] = float(fs.group(1))
    dev = _DEV_RE.search(text)
    if dev:
        mem["dev"] = dev.group(1)
        mem["dev_total"] = float(dev.group(2))
        mem["dev_free_at_load"] = float(dev.group(3))
    _mem_cache[name] = mem
    return mem


def status_of(srv, proc):
    """Return a short status string for a server."""
    code = proc.poll()
    if code is not None:
        return "EXIT(%d)" % code
    sc, _ = http_get(srv["port"], "/health")
    if sc == 200:
        return "UP"
    if sc in (503, 500):
        return "LOADING"
    return "START"


def fmt_int(metrics, key):
    v = metrics.get(key)
    return "-" if v is None else str(int(v))


def fmt_uptime(seconds):
    return str(timedelta(seconds=int(seconds)))


def clear_screen():
    # ASCII control sequences: move cursor home + clear to end of screen.
    sys.stdout.write("\033[H\033[J")


def _mib(v):
    return "-" if v is None else "%.0f" % v


def fmt_rate(metrics, key):
    """Format a tokens/second gauge from /metrics; '-' when absent or zero."""
    v = metrics.get(key)
    return "-" if not v else "%.1f" % v


def render(servers, procs, started_at):
    clear_screen()
    now = time.time()
    width = 78
    print("=" * width)
    print(" Harlequin LLM servers   uptime %-12s   refresh %.0fs   Ctrl-C to stop"
          % (fmt_uptime(now - started_at), REFRESH_SECONDS))
    print(" TOTAL MB from llama-server load logs (-lv %d); PP/TG tok/s from /metrics"
          % VERBOSITY)
    print("=" * width)
    # TOTAL MB = the actual VRAM/RAM the server holds (model + KV/state + compute
    # buffers). PP/TG = average prompt-processing and token-generation throughput
    # (tokens/s). GEN_TK = total tokens generated. All from llama-server itself.
    print("%-6s %-6s %-7s %-8s %9s %8s %8s %9s" % (
        "NAME", "PORT", "PID", "STATUS", "TOTAL MB", "PP_T/S", "TG_T/S", "GEN_TK"))
    print("-" * width)

    notes = []
    dev_seen = None
    for srv in servers:
        proc = procs[srv["name"]]
        st = status_of(srv, proc)
        pp = tg = gen_tk = "-"
        if st == "UP":
            sc, body = http_get(srv["port"], "/metrics")
            if sc == 200:
                m = parse_metrics(body)
                pp = fmt_rate(m, "llamacpp:prompt_tokens_seconds")
                tg = fmt_rate(m, "llamacpp:predicted_tokens_seconds")
                gen_tk = fmt_int(m, "llamacpp:tokens_predicted_total")
        mem = parse_memory(srv["name"], log_path(srv)) if proc.poll() is None else {}
        if mem.get("dev"):
            dev_seen = mem  # keep a device line to show in the footer
        print("%-6s %-6d %-7d %-8s %9s %8s %8s %9s" % (
            srv["name"], srv["port"], proc.pid, st,
            _mib(mem.get("total")), pp, tg, gen_tk))
        if st.startswith("EXIT"):
            notes.append("%-6s exited - see %s" % (srv["name"], log_path(srv)))

    print("-" * width)
    total_all = sum(_mem_cache.get(s["name"], {}).get("total", 0.0) for s in servers)
    print(" TOTAL model footprint across all servers: %.0f MiB (%.2f GiB)"
          % (total_all, total_all / 1024))
    if dev_seen:
        print(" GPU device: %s, %.0f MiB total (%.0f MiB free when last model loaded)"
              % (dev_seen["dev"], dev_seen["dev_total"], dev_seen["dev_free_at_load"]))
    print(" models: " + ", ".join("%s=%s" % (s["name"], s["model"]) for s in servers))
    if notes:
        print("")
        print(" NOTES:")
        for n in notes:
            print("   " + n)
    sys.stdout.flush()


def log_path(srv):
    return os.path.join(LOG_DIR, "%s.log" % srv["name"])


def start_all(servers):
    os.makedirs(LOG_DIR, exist_ok=True)
    procs = {}
    for srv in servers:
        cmd = build_command(srv)
        logf = open(log_path(srv), "w")
        print("starting %-6s -> %s" % (srv["name"], " ".join(cmd)))
        # start_new_session: children get their own process group so a Ctrl-C in
        # the terminal does not race our own clean shutdown.
        procs[srv["name"]] = subprocess.Popen(
            cmd, stdout=logf, stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    return procs


def stop_all(servers, procs):
    print("\nstopping servers...")
    for srv in servers:
        proc = procs.get(srv["name"])
        if proc and proc.poll() is None:
            proc.terminate()
    deadline = time.time() + 10
    for srv in servers:
        proc = procs.get(srv["name"])
        if not proc:
            continue
        remaining = max(0.0, deadline - time.time())
        try:
            proc.wait(timeout=remaining)
            print("  %-6s stopped" % srv["name"])
        except subprocess.TimeoutExpired:
            proc.kill()
            print("  %-6s killed (did not stop in time)" % srv["name"])


def main():
    if not LLAMA_SERVER or not os.path.exists(LLAMA_SERVER):
        sys.exit("error: llama-server binary not found (set LLAMA_SERVER in the script)")
    for srv in SERVERS:
        path = os.path.join(MODEL_DIR, srv["model"])
        if not os.path.exists(path):
            sys.exit("error: model file not found: %s" % path)

    procs = start_all(SERVERS)
    started_at = time.time()
    try:
        while True:
            render(SERVERS, procs, started_at)
            time.sleep(REFRESH_SECONDS)
    except KeyboardInterrupt:
        pass
    finally:
        stop_all(SERVERS, procs)


if __name__ == "__main__":
    main()
