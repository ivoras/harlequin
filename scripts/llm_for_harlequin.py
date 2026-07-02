#!/usr/bin/env python3
"""
llm_for_harlequin.py - launch and monitor the three llama.cpp servers Harlequin uses:

  * chat   - the big model that drives the conversation
  * aux    - a tiny, fast model for auxiliary delegate work (WebFetch / WebFetchDOM
             analysis, etc.); given a generous 24K context
  * embed  - the embedding model

It starts all three, prints a live Unicode dashboard (status, memory bars, slot
utilisation, token counters and sparkline history (print_timing log lines; hold last
rate for 60s when idle, then 0), and on Ctrl-C shuts all of them down cleanly.

Use --monitor-only (or -m) to attach to servers that are already running without
restarting them. Set HARLEQUIN_LLM_LOG_DIR if logs live outside scripts/logs/
(default also checks ~/LLM/logs/). --model-dir (or HARLEQUIN_LLM_MODEL_DIR) sets
the GGUF directory.

Interactive keys: [p]ause refresh, [r]/[a]/[e] restart chat/aux/embed,
[1]/[2]/[3] toggle log tail, [?] help, Ctrl-C quit.

To change models / ports / flags, edit the SERVERS list below - that is the only
section you should normally need to touch.

IMPORTANT: keep this script stdlib-only; no external dependencies, no vendored code.
"""

import argparse
import json
import os
import re
import select
import shutil
import signal
import subprocess
import sys
import termios
import tty
import time
import urllib.error
import urllib.request
from collections import deque
from datetime import timedelta

# --------------------------------------------------------------------------- #
# Configuration - edit this block to change models, ports, or llama.cpp flags. #
# --------------------------------------------------------------------------- #

# Directory holding the .gguf files and where per-server logs are written.
# Overridden by --model-dir or HARLEQUIN_LLM_MODEL_DIR in main().
MODEL_DIR = os.environ.get(
    "HARLEQUIN_LLM_MODEL_DIR", os.path.dirname(os.path.abspath(__file__))
)
LOG_DIR = os.environ.get("HARLEQUIN_LLM_LOG_DIR", os.path.join(MODEL_DIR, "logs"))

# Path to the llama.cpp server binary (found on PATH by default).
LLAMA_SERVER = shutil.which("llama-server") or os.path.expanduser(
    "~/llama.cpp/build/bin/llama-server"
)

HOST = "0.0.0.0"           # bind address passed to every server
REFRESH_SECONDS = 2.0      # dashboard refresh interval
HISTORY_MINUTES = 15.0     # sparkline window (overridden by --history-minutes)
HISTORY_SAMPLES = int(HISTORY_MINUTES * 60 / REFRESH_SECONDS)
STALE_RATE_SECONDS = 60.0  # hold last log PP/TG rate; then show 0 until new log lines
LOG_TAIL_LINES = 8

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

# ANSI styling (disabled when stdout is not a TTY).
def _sty():
    if not sys.stdout.isatty():
        return {k: "" for k in (
            "reset", "bold", "dim", "red", "green", "yellow", "blue",
            "magenta", "cyan", "white", "bg_blue",
        )}
    return {
        "reset": "\033[0m",
        "bold": "\033[1m",
        "dim": "\033[2m",
        "red": "\033[31m",
        "green": "\033[32m",
        "yellow": "\033[33m",
        "blue": "\033[34m",
        "magenta": "\033[35m",
        "cyan": "\033[36m",
        "white": "\033[37m",
        "bg_blue": "\033[44m",
    }


S = _sty()

# Unicode box-drawing and sparkline glyphs.
_ANSI_RE = re.compile(r"\033\[[0-9;]*m|[\x00-\x1f\x7f]")


def visible_len(text):
    return len(_ANSI_RE.sub("", text))


def trunc_visible(text, max_len):
    """Truncate so visible (non-ANSI) length <= max_len."""
    if visible_len(text) <= max_len:
        return text
    plain = _ANSI_RE.sub("", text)
    return plain[:max_len]


BOX = {
    "tl": "╔", "tr": "╗", "bl": "╚", "br": "╝",
    "h": "═", "v": "║",
    "lt": "╠", "rt": "╣",
    "inner_tl": "┌", "inner_tr": "┐", "inner_bl": "└", "inner_br": "┘",
    "inner_h": "─", "inner_v": "│",
    "dot": "●", "ring": "◉", "diamond": "◈",
    "bar_fill": "█", "bar_mid": "▓", "bar_light": "▒", "bar_empty": "░",
    "spark": "▁▂▃▄▅▆▇█",
}


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
_SPEC_RE = re.compile(
    r"#gen drafts =\s+(\d+),\s+#acc drafts =\s+(\d+).*#mean acc len =\s+([\d.]+)"
)
_SLOT_SPEC_RE = re.compile(
    r"draft acceptance = ([\d.]+)\s+\(\s*(\d+) accepted / (\d+) generated\), mean len =\s+([\d.]+)"
)
# slot print_timing lines (verbosity >= 4); sparklines prefer these over /metrics gauges.
_TIMING_PP_PROGRESS_RE = re.compile(
    r"print_timing:.*prompt processing,.*?/\s*([\d.]+)\s*tokens per second"
)
_TIMING_PP_EVAL_RE = re.compile(
    r"print_timing:.*prompt eval time =.*?,\s*([\d.]+)\s*tokens per second"
)
_TIMING_TG_LIVE_RE = re.compile(
    r"print_timing:.*\btg\s*=\s*([\d.]+)\s*t/s"
)
_TIMING_TG_EVAL_RE = re.compile(
    r"print_timing:.*\|\s+eval time =.*?,\s*([\d.]+)\s*tokens per second"
)

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


def parse_spec_stats(path):
    """Latest ngram speculative-decoding stats from the log tail."""
    try:
        with open(path, "rb") as f:
            f.seek(0, 2)
            f.seek(max(0, f.tell() - 131072))
            text = f.read().decode("utf-8", "replace")
    except OSError:
        return {}
    out = {}
    stats = _SPEC_RE.findall(text)
    if stats:
        gen, acc, mean = stats[-1]
        gen, acc = int(gen), int(acc)
        out["draft_gen"] = gen
        out["draft_acc"] = acc
        out["mean_acc_len"] = float(mean)
        out["draft_rate"] = (acc / gen) if gen else 0.0
    slots = _SLOT_SPEC_RE.findall(text)
    if slots:
        rate, acc, gen, mean = slots[-1]
        out["last_slot_rate"] = float(rate)
        out["last_slot_acc"] = int(acc)
        out["last_slot_gen"] = int(gen)
        out["last_slot_mean"] = float(mean)
    return out


def tail_log(path, n=LOG_TAIL_LINES):
    try:
        with open(path, "rb") as f:
            f.seek(0, 2)
            block = min(f.tell(), 65536)
            f.seek(-block, 2)
            lines = f.read().decode("utf-8", "replace").splitlines()
        return lines[-n:] if lines else ["(empty log)"]
    except OSError:
        return ["(log not found: %s)" % path]


def read_log_increment(path, pos):
    """Read log bytes from pos; return (text, new_pos). pos None -> start at EOF."""
    try:
        with open(path, "rb") as f:
            f.seek(0, 2)
            size = f.tell()
            if pos is None:
                return "", size
            if pos > size:  # truncated / rotated
                pos = max(0, size - 65536)
            f.seek(pos)
            raw = f.read()
            return raw.decode("utf-8", "replace"), pos + len(raw)
    except OSError:
        return "", pos if pos is not None else 0


def parse_timing_rates(text):
    """Latest PP/TG tokens/s from new print_timing log lines."""
    pp = tg = None
    for line in text.splitlines():
        if "print_timing" not in line:
            continue
        m = _TIMING_PP_PROGRESS_RE.search(line)
        if m:
            pp = float(m.group(1))
            continue
        m = _TIMING_PP_EVAL_RE.search(line)
        if m:
            pp = float(m.group(1))
            continue
        m = _TIMING_TG_LIVE_RE.search(line)
        if m:
            tg = float(m.group(1))
            continue
        m = _TIMING_TG_EVAL_RE.search(line)
        if m:
            tg = float(m.group(1))
    return pp, tg


def find_pid_on_port(port):
    try:
        out = subprocess.check_output(
            ["ss", "-lptn", "sport", "=", ":%d" % port],
            text=True, stderr=subprocess.DEVNULL,
        )
        m = re.search(r"pid=(\d+)", out)
        return int(m.group(1)) if m else None
    except (subprocess.CalledProcessError, FileNotFoundError, ValueError):
        return None


class KeyReader:
    """Non-blocking single-key input when stdin is a TTY."""

    def __init__(self):
        self.enabled = sys.stdin.isatty()
        self._old = None
        if self.enabled:
            self._old = termios.tcgetattr(sys.stdin)
            tty.setcbreak(sys.stdin.fileno())

    def read(self):
        if not self.enabled:
            return None
        if select.select([sys.stdin], [], [], 0)[0]:
            return sys.stdin.read(1)
        return None

    def close(self):
        if self._old is not None:
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, self._old)
            self._old = None


def refresh_monitor_pids(servers, procs):
    for srv in servers:
        pid = find_pid_on_port(srv["port"])
        if pid:
            procs[srv["name"]].pid = pid


def stop_server(srv, proc, monitor_only):
    if monitor_only:
        pid = find_pid_on_port(srv["port"])
        if pid:
            try:
                os.kill(pid, signal.SIGTERM)
            except OSError:
                pass
            deadline = time.time() + 8
            while time.time() < deadline and find_pid_on_port(srv["port"]):
                time.sleep(0.2)
            if find_pid_on_port(srv["port"]):
                try:
                    os.kill(pid, signal.SIGKILL)
                except OSError:
                    pass
        return
    if proc and proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=8)
        except subprocess.TimeoutExpired:
            proc.kill()


def restart_server(srv, procs, monitor_only, history=None):
    stop_server(srv, procs.get(srv["name"]), monitor_only)
    _mem_cache.pop(srv["name"], None)
    if history:
        history.reset_log(srv["name"])
    os.makedirs(LOG_DIR, exist_ok=True)
    logf = open(log_path(srv), "w")
    cmd = build_command(srv)
    procs[srv["name"]] = subprocess.Popen(
        cmd, stdout=logf, stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    return "restarted %s (pid %d)" % (srv["name"], procs[srv["name"]].pid)


def check_alerts(prev, curr):
    """Return new alert strings and ring the terminal bell on failures."""
    out = []
    for name, st in curr.items():
        was = prev.get(name)
        if was in ("UP", "LOADING") and st not in ("UP", "LOADING"):
            out.append("%s: %s" % (name, st))
            sys.stdout.write("\a")
            sys.stdout.flush()
    return out


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


def fmt_int(v):
    if v is None:
        return "-"
    if v >= 1_000_000:
        return "%.1fM" % (v / 1_000_000)
    if v >= 10_000:
        return "%.1fk" % (v / 1_000)
    return str(int(v))


def fmt_uptime(seconds):
    return str(timedelta(seconds=int(seconds)))


def clear_screen():
    sys.stdout.write("\033[H\033[J")


def fmt_rate(v):
    return "-" if not v else "%.1f" % v


def short_model(path):
    base = os.path.basename(path)
    if len(base) <= 34:
        return base
    return base[:15] + "…" + base[-16:]


def sparkline(values, width=None):
    """Render a Unicode block sparkline; width defaults to len(values)."""
    if not values:
        return ""
    width = width or len(values)
    if len(values) > width:
        values = values[-width:]
    elif len(values) < width:
        pad = [0.0] * (width - len(values))
        values = pad + list(values)
    mx = max(values) or 1.0
    blocks = BOX["spark"]
    return "".join(blocks[min(len(blocks) - 1, int(v / mx * (len(blocks) - 1)))] for v in values)


def stacked_bar(parts, width, labels=None):
    """Horizontal proportional bar from {label: value} dict (values in same units)."""
    total = sum(parts.values())
    if total <= 0:
        return BOX["bar_empty"] * width, ""
    segs = []
    legend_bits = []
    remaining = width
    items = list(parts.items())
    for i, (label, val) in enumerate(items):
        if i == len(items) - 1:
            n = remaining
        else:
            n = max(0, int(round(val / total * width)))
            remaining -= n
        if n > 0:
            segs.append((label, n))
        if labels and val > 0:
            legend_bits.append("%s %.0f" % (label, val))
    bar = []
    for label, n in segs:
        ch = {"model": BOX["bar_fill"], "state": BOX["bar_mid"], "compute": BOX["bar_light"]}.get(
            label, BOX["bar_fill"]
        )
        bar.append(ch * n)
    text = "".join(bar)
    if len(text) < width:
        text += BOX["bar_empty"] * (width - len(text))
    return text[:width], "  ".join(legend_bits)


def pct_bar(used, total, width):
    if not total or total <= 0:
        return BOX["bar_empty"] * width, 0.0
    pct = min(1.0, used / total)
    filled = int(round(pct * width))
    return BOX["bar_fill"] * filled + BOX["bar_empty"] * (width - filled), pct


def status_badge(st):
    if st == "UP":
        return "%s%s %s%s" % (S["green"], BOX["dot"], st, S["reset"])
    if st == "LOADING":
        return "%s%s %s%s" % (S["yellow"], BOX["ring"], st, S["reset"])
    if st.startswith("EXIT"):
        return "%s%s %s%s" % (S["red"], "✖", st, S["reset"])
    return "%s%s %s%s" % (S["dim"], "○", st, S["reset"])


def parse_slots(port):
    sc, body = http_get(port, "/slots", timeout=1.5)
    if sc != 200 or not body:
        return []
    try:
        return json.loads(body)
    except json.JSONDecodeError:
        return []


def slot_summary(slots):
    if not slots:
        return "no slots", []
    busy = sum(1 for s in slots if s.get("is_processing"))
    lines = []
    for s in slots:
        sid = s.get("id", "?")
        n_ctx = s.get("n_ctx") or 1
        prompt = s.get("n_prompt_tokens") or 0
        fill = min(100, int(100 * prompt / n_ctx))
        spec = "spec" if s.get("speculative") else "std"
        if s.get("is_processing"):
            state = "%s%s BUSY%s" % (S["cyan"], BOX["bar_fill"], S["reset"])
        else:
            state = "%sidle%s" % (S["dim"], S["reset"])
        lines.append("slot %d %s ctx %3d%% %s" % (sid, state, fill, spec))
    head = "%d/%d active" % (busy, len(slots))
    return head, lines


def context_arg(srv):
    for i, a in enumerate(srv.get("args", [])):
        if a in ("-c", "--ctx-size") and i + 1 < len(srv["args"]):
            try:
                return int(srv["args"][i + 1])
            except ValueError:
                pass
    return None


class History:
    """Per-server rolling throughput samples for sparklines."""

    def __init__(self, servers, maxlen=HISTORY_SAMPLES):
        self.maxlen = maxlen
        self.data = {
            s["name"]: {
                "pp": deque(maxlen=maxlen),
                "tg": deque(maxlen=maxlen),
                "prompt_delta": deque(maxlen=maxlen),  # embed: /metrics fallback
                "_log_pos": None,
                "_pp_from_log": False,
                "_tg_from_log": False,
                "_last_pp": 0.0,
                "_last_tg": 0.0,
                "_last_pp_at": None,
                "_last_tg_at": None,
                "_last_prompt": None,
                "_last_ts": None,
            }
            for s in servers
        }

    def reset_log(self, name):
        d = self.data[name]
        d["_log_pos"] = None
        d["_pp_from_log"] = False
        d["_tg_from_log"] = False
        d["_last_pp"] = 0.0
        d["_last_tg"] = 0.0
        d["_last_pp_at"] = None
        d["_last_tg_at"] = None

    @staticmethod
    def _rate_sample(new_log, last_val, last_at, now):
        """Pick sparkline sample: fresh log rate, held last rate, or 0 when stale."""
        if new_log is not None:
            return new_log, now, True
        if last_at is not None and (now - last_at) < STALE_RATE_SECONDS:
            return last_val, last_at, True
        return 0.0, last_at, False

    def update(self, name, metrics, now, log_path=None, kind="chat"):
        d = self.data[name]

        pp_log = tg_log = None
        if log_path and kind != "embed":
            chunk, d["_log_pos"] = read_log_increment(log_path, d["_log_pos"])
            pp_log, tg_log = parse_timing_rates(chunk)

        if kind == "embed":
            prompt = metrics.get("llamacpp:prompt_tokens_total")
            if d["_last_ts"] is not None and prompt is not None and d["_last_prompt"] is not None:
                dt = now - d["_last_ts"]
                if dt > 0:
                    d["prompt_delta"].append(max(0.0, (prompt - d["_last_prompt"]) / dt))
            if prompt is not None:
                d["_last_prompt"] = prompt
            d["_last_ts"] = now
            return

        pp_sample, pp_at, pp_from_log = self._rate_sample(
            pp_log, d["_last_pp"], d["_last_pp_at"], now,
        )
        tg_sample, tg_at, tg_from_log = self._rate_sample(
            tg_log, d["_last_tg"], d["_last_tg_at"], now,
        )
        if pp_log is not None:
            d["_last_pp"] = pp_log
            d["_last_pp_at"] = now
        elif not pp_from_log:
            d["_last_pp"] = 0.0
        else:
            d["_last_pp_at"] = pp_at
        if tg_log is not None:
            d["_last_tg"] = tg_log
            d["_last_tg_at"] = now
        elif not tg_from_log:
            d["_last_tg"] = 0.0
        else:
            d["_last_tg_at"] = tg_at
        d["_pp_from_log"] = pp_from_log
        d["_tg_from_log"] = tg_from_log
        d["pp"].append(pp_sample)
        d["tg"].append(tg_sample)


def term_width(default=88):
    try:
        return max(72, shutil.get_terminal_size().columns)
    except OSError:
        return default


def boxed_line(inner, width):
    """Inner content padded to fit inside ║ ... ║ (ANSI-aware)."""
    inner = trunc_visible(inner, width - 4)
    pad = max(0, width - 4 - visible_len(inner))
    return "%s %s%s %s" % (BOX["v"], inner, " " * pad, BOX["v"])


def collect_frame(servers, procs, started_at, history, ui=None):
    """Gather all server/log/metrics state before the screen is cleared."""
    ui = ui or {}
    now = time.time()
    width = term_width()
    inner = width - 4
    spark_w = min(28, max(12, inner // 4))

    notes = []
    dev_seen = None
    total_mem = 0.0
    statuses = {}
    server_rows = []

    for srv in servers:
        proc = procs[srv["name"]]
        st = status_of(srv, proc)
        statuses[srv["name"]] = st
        lp = log_path(srv)
        mem = parse_memory(srv["name"], lp) if proc.poll() is None else {}
        if mem.get("dev"):
            dev_seen = mem
        if mem.get("total"):
            total_mem += mem["total"]

        metrics = {}
        slots = []
        if st == "UP":
            sc, body = http_get(srv["port"], "/metrics")
            if sc == 200:
                metrics = parse_metrics(body)
            history.update(
                srv["name"], metrics, now,
                log_path=lp, kind=srv["kind"],
            )
            slots = parse_slots(srv["port"])

        hd = history.data[srv["name"]]
        slot_head, slot_lines = slot_summary(slots)
        spec = parse_spec_stats(lp) if srv["kind"] == "chat" and st == "UP" else {}

        server_rows.append({
            "srv": srv,
            "proc": proc,
            "st": st,
            "lp": lp,
            "mem": mem,
            "metrics": metrics,
            "hd": hd,
            "pp": hd["pp"][-1] if hd["pp"] else 0.0,
            "tg": hd["tg"][-1] if hd["tg"] else 0.0,
            "pp_src": "log" if hd.get("_pp_from_log") else "idle",
            "tg_src": "log" if hd.get("_tg_from_log") else "idle",
            "slot_head": slot_head,
            "slot_lines": slot_lines,
            "ctx_cfg": context_arg(srv) or (slots[0].get("n_ctx") if slots else None),
            "spec": spec,
        })
        if st.startswith("EXIT"):
            notes.append("%-6s exited — see %s" % (srv["name"], lp))

    log_tail = []
    log_view = ui.get("log_view")
    if log_view:
        srv = next((s for s in servers if s["name"] == log_view), None)
        if srv:
            log_tail = tail_log(log_path(srv))

    return {
        "now": now,
        "started_at": started_at,
        "width": width,
        "inner": inner,
        "spark_w": spark_w,
        "statuses": statuses,
        "server_rows": server_rows,
        "notes": notes,
        "dev_seen": dev_seen,
        "total_mem": total_mem,
        "log_view": log_view,
        "log_tail": log_tail,
    }


def draw_frame(frame, ui=None):
    """Paint a frame collected by collect_frame (screen must be cleared first)."""
    ui = ui or {}
    width = frame["width"]
    inner = frame["inner"]
    spark_w = frame["spark_w"]

    title = "%s %s HARLEQUIN LLM MONITOR %s" % (
        S["bold"], BOX["diamond"], S["reset"],
    )
    pause_tag = "  %s⏸ PAUSED%s" % (S["yellow"], S["reset"]) if ui.get("paused") else ""
    meta = "uptime %s  │  refresh %.0fs  │  history %.0fm%s  │  Ctrl-C quit" % (
        fmt_uptime(frame["now"] - frame["started_at"]), REFRESH_SECONDS,
        HISTORY_MINUTES, pause_tag,
    )
    print("%s%s%s" % (BOX["tl"], BOX["h"] * (width - 2), BOX["tr"]))
    print(boxed_line(title + "  " + S["dim"] + meta + S["reset"], width))

    for alert in ui.get("alerts", [])[-3:]:
        print(boxed_line(S["red"] + S["bold"] + " ⚠ ALERT: " + alert + S["reset"], width))

    if ui.get("toast"):
        print(boxed_line(S["cyan"] + " " + ui["toast"] + S["reset"], width))

    print("%s%s%s" % (BOX["lt"], BOX["h"] * (width - 2), BOX["rt"]))

    for row in frame["server_rows"]:
        srv = row["srv"]
        proc = row["proc"]
        st = row["st"]
        mem = row["mem"]
        hd = row["hd"]
        pp, tg = row["pp"], row["tg"]
        pp_src, tg_src = row["pp_src"], row["tg_src"]
        metrics = row["metrics"]
        gen_tk = metrics.get("llamacpp:tokens_predicted_total")
        pr_tk = metrics.get("llamacpp:prompt_tokens_total")
        peak = metrics.get("llamacpp:n_tokens_max")
        req_on = int(metrics.get("llamacpp:requests_processing") or 0)
        req_def = int(metrics.get("llamacpp:requests_deferred") or 0)
        decodes = metrics.get("llamacpp:n_decode_total")
        busy_slots = metrics.get("llamacpp:n_busy_slots_per_decode")
        slot_head = row["slot_head"]
        slot_lines = row["slot_lines"]
        ctx_cfg = row["ctx_cfg"]

        panel_w = inner - 2
        name_line = "%s %s:%d %s pid %s" % (
            S["bold"] + srv["name"].upper() + S["reset"],
            S["dim"], srv["port"], S["reset"],
            proc.pid if proc.poll() is None else "-",
        )
        hdr = "%s %s  %s" % (
            status_badge(st), name_line,
            S["dim"] + short_model(srv["model"]) + S["reset"],
        )
        print(boxed_line("", width))
        print(boxed_line(hdr, width))

        mem_bar_w = min(64, panel_w - 18)
        if mem.get("total"):
            parts = {k: mem.get(k, 0) for k in ("model", "state", "compute") if mem.get(k)}
            bar, legend = stacked_bar(parts, mem_bar_w)
            print(boxed_line(
                "  %smem%s %s%s%s %.0f MiB  %s" % (
                    S["dim"], S["reset"], S["magenta"], bar, S["reset"],
                    mem["total"], legend,
                ),
                width,
            ))
        else:
            print(boxed_line(
                "  %smem%s %s (waiting for -lv %d load log)" % (
                    S["dim"], S["reset"], BOX["bar_empty"] * mem_bar_w, VERBOSITY,
                ),
                width,
            ))
        print(boxed_line("  %sslots%s %s" % (S["dim"], S["reset"], slot_head), width))

        pp_spark = sparkline(list(hd["pp"]), spark_w)
        tg_spark = sparkline(list(hd["tg"]), spark_w)
        if srv["kind"] == "embed":
            print(boxed_line(
                "  %sdecode rate%s %s" % (
                    S["cyan"], S["reset"],
                    sparkline(list(hd["prompt_delta"]) or [0], spark_w),
                ),
                width,
            ))
            print(boxed_line(
                "  %sn_decode%s %s" % (S["dim"], S["reset"], fmt_int(decodes)),
                width,
            ))
        else:
            print(boxed_line(
                "  %sPP%s %5s t/s%s  %s%s%s   %sTG%s %5s t/s%s  %s%s%s" % (
                    S["cyan"], S["reset"], fmt_rate(pp),
                    S["dim"] + (" log" if pp_src == "log" else " idle") + S["reset"],
                    S["blue"], pp_spark, S["reset"],
                    S["green"], S["reset"], fmt_rate(tg),
                    S["dim"] + (" log" if tg_src == "log" else " idle") + S["reset"],
                    S["green"], tg_spark, S["reset"],
                ),
                width,
            ))

        row1 = "  prompt %s  │  generated %s  │  peak ctx %s" % (
            fmt_int(pr_tk), fmt_int(gen_tk), fmt_int(peak),
        )
        row2 = "  active req %d  │  deferred %d" % (req_on, req_def)
        if srv["kind"] != "embed" and busy_slots:
            row2 += "  │  busy slots/decode %.2f" % busy_slots
        if ctx_cfg:
            row2 += "  │  n_ctx %s" % fmt_int(ctx_cfg)
        print(boxed_line(S["dim"] + row1 + S["reset"], width))
        print(boxed_line(S["dim"] + row2 + S["reset"], width))

        for sl in slot_lines[:2]:
            print(boxed_line("  " + sl, width))

        spec = row["spec"]
        if spec:
            spec_line = "  %sspeculative%s ngram-map-k4v" % (S["dim"], S["reset"])
            if "draft_rate" in spec:
                spec_line += "  drafts %d/%d (%.0f%%)  mean len %.1f" % (
                    spec["draft_acc"], spec["draft_gen"], spec["draft_rate"] * 100,
                    spec.get("mean_acc_len", 0),
                )
            if "last_slot_rate" in spec:
                spec_line += "  │  last slot %.0f%% (%d/%d)" % (
                    spec["last_slot_rate"] * 100,
                    spec["last_slot_acc"], spec["last_slot_gen"],
                )
            print(boxed_line(S["dim"] + spec_line + S["reset"], width))

    total_mem = frame["total_mem"]
    dev_seen = frame["dev_seen"]
    print("%s%s%s" % (BOX["lt"], BOX["h"] * (width - 2), BOX["rt"]))
    foot = " Σ footprint %.2f GiB (%d MiB) across %d servers" % (
        total_mem / 1024, int(total_mem), len(frame["server_rows"]),
    )
    print(boxed_line(S["bold"] + foot + S["reset"], width))
    if dev_seen:
        used_est = dev_seen["dev_total"] - dev_seen.get("dev_free_at_load", 0) + total_mem
        gpu_bar, gpu_pct = pct_bar(used_est, dev_seen["dev_total"], 24)
        print(boxed_line(
            S["bold"] + " GPU %s" % dev_seen["dev"] + S["reset"] +
            "  %s%s%s %.0f MiB / %.0f MiB (%d%%)" % (
                S["yellow"], gpu_bar, S["reset"],
                used_est, dev_seen["dev_total"], int(gpu_pct * 100),
            ),
            width,
        ))

    legend = (
        " %s█%s model  %s▓%s KV/state  %s▒%s compute  │  "
        "sparklines: print_timing log (%d samples ~%.0fm), hold %.0fs then 0"
    ) % (
        S["magenta"], S["reset"], S["magenta"], S["reset"],
        S["magenta"], S["reset"],
        HISTORY_SAMPLES, HISTORY_MINUTES, STALE_RATE_SECONDS,
    )
    print(boxed_line(S["dim"] + legend + S["reset"], width))

    help_line = " keys: [p]ause  [r/a/e] restart  [1-3] logs  [?] help"
    if ui.get("show_help"):
        help_line += "  — restart kills & relaunches; monitor-only can restart too"
    print(boxed_line(S["dim"] + help_line + S["reset"], width))

    if frame["log_view"] and frame["log_tail"]:
        print(boxed_line(S["bold"] + " log tail: %s" % frame["log_view"] + S["reset"], width))
        for line in frame["log_tail"]:
            print(boxed_line(" " + trunc_visible(line.strip(), width - 6), width))

    notes = frame["notes"]
    if notes:
        print(boxed_line("", width))
        for n in notes:
            print(boxed_line(S["red"] + " ⚠ " + n + S["reset"], width))

    print("%s%s%s" % (BOX["bl"], BOX["h"] * (width - 2), BOX["br"]))
    sys.stdout.flush()


def render(servers, procs, started_at, history, ui=None):
    frame = collect_frame(servers, procs, started_at, history, ui)
    clear_screen()
    draw_frame(frame, ui)
    return frame


def log_path(srv):
    primary = os.path.join(LOG_DIR, "%s.log" % srv["name"])
    if os.path.exists(primary):
        return primary
    alt = os.path.expanduser("~/LLM/logs/%s.log" % srv["name"])
    if os.path.exists(alt):
        return alt
    return primary


def start_all(servers):
    os.makedirs(LOG_DIR, exist_ok=True)
    procs = {}
    for srv in servers:
        cmd = build_command(srv)
        logf = open(log_path(srv), "w")
        print("starting %-6s -> %s" % (srv["name"], " ".join(cmd)))
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


def monitor_procs(servers):
    """Stub process handles for --monitor-only (servers already running)."""
    class _Proc:
        def __init__(self):
            self.pid = 0

        def poll(self):
            return None

    return {s["name"]: _Proc() for s in servers}


def parse_args():
    p = argparse.ArgumentParser(description="Launch and monitor Harlequin llama.cpp servers")
    p.add_argument("-m", "--monitor-only", action="store_true",
                   help="attach to running servers; do not stop them on exit")
    p.add_argument("--model-dir", default=os.environ.get("HARLEQUIN_LLM_MODEL_DIR"),
                   help="directory with .gguf files (env: HARLEQUIN_LLM_MODEL_DIR)")
    p.add_argument("--refresh", type=float, default=REFRESH_SECONDS,
                   help="dashboard refresh interval in seconds")
    p.add_argument("--history-minutes", type=float, default=HISTORY_MINUTES,
                   help="sparkline history window in minutes")
    return p.parse_args()


def handle_key(key, ui, servers, procs, monitor_only, history=None):
    """Process one keypress; may set ui toast and trigger restarts."""
    if not key:
        return
    if key == "p":
        ui["paused"] = not ui.get("paused")
        ui["toast"] = "refresh paused" if ui["paused"] else "refresh resumed"
    elif key == "?":
        ui["show_help"] = not ui.get("show_help")
    elif key in ("1", "2", "3"):
        idx = int(key) - 1
        if idx < len(servers):
            name = servers[idx]["name"]
            ui["log_view"] = None if ui.get("log_view") == name else name
            ui["toast"] = "log tail: %s" % (ui["log_view"] or "off")
    elif key in ("r", "a", "e"):
        mapping = {"r": "chat", "a": "aux", "e": "embed"}
        name = mapping[key]
        srv = next((s for s in servers if s["name"] == name), None)
        if srv:
            try:
                ui["toast"] = restart_server(srv, procs, monitor_only, history)
            except OSError as e:
                ui["toast"] = "restart failed: %s" % e
    else:
        return
    ui["toast_until"] = time.time() + 3.0


def wait_after_redraw(keys, ui, servers, procs, started_at, history, monitor_only):
    """Hold the last frame for REFRESH_SECONDS; poll keys during the wait."""
    deadline = time.time() + REFRESH_SECONDS
    while time.time() < deadline:
        key = keys.read()
        if not key:
            time.sleep(0.05)
            continue
        handle_key(key, ui, servers, procs, monitor_only, history)
        render(servers, procs, started_at, history, ui)
        deadline = time.time() + REFRESH_SECONDS


def main():
    global MODEL_DIR, LOG_DIR, REFRESH_SECONDS, HISTORY_MINUTES, HISTORY_SAMPLES

    args = parse_args()
    if args.model_dir:
        MODEL_DIR = os.path.abspath(args.model_dir)
    LOG_DIR = os.environ.get("HARLEQUIN_LLM_LOG_DIR", os.path.join(MODEL_DIR, "logs"))
    REFRESH_SECONDS = args.refresh
    HISTORY_MINUTES = args.history_minutes
    HISTORY_SAMPLES = max(30, int(HISTORY_MINUTES * 60 / REFRESH_SECONDS))

    monitor_only = args.monitor_only
    if not monitor_only:
        if not LLAMA_SERVER or not os.path.exists(LLAMA_SERVER):
            sys.exit("error: llama-server binary not found (set LLAMA_SERVER in the script)")
        for srv in SERVERS:
            path = os.path.join(MODEL_DIR, srv["model"])
            if not os.path.exists(path):
                sys.exit("error: model file not found: %s" % path)

    started_at = time.time()
    history = History(SERVERS)
    procs = monitor_procs(SERVERS) if monitor_only else start_all(SERVERS)
    keys = KeyReader()
    ui = {"paused": False, "log_view": "chat", "show_help": False, "alerts": [], "toast": ""}
    prev_status = {}

    if monitor_only:
        refresh_monitor_pids(SERVERS, procs)
        print("monitor-only: attaching to existing servers (Ctrl-C to exit dashboard)")
        time.sleep(0.5)

    try:
        while True:
            now = time.time()
            if ui.get("toast_until", 0) < now:
                ui["toast"] = ""

            if monitor_only:
                refresh_monitor_pids(SERVERS, procs)

            if ui.get("paused"):
                key = keys.read()
                if key:
                    handle_key(key, ui, SERVERS, procs, monitor_only, history)
                    if not ui.get("paused"):
                        render(SERVERS, procs, started_at, history, ui)
                        wait_after_redraw(keys, ui, SERVERS, procs, started_at, history, monitor_only)
                else:
                    time.sleep(0.15)
                continue

            frame = render(SERVERS, procs, started_at, history, ui)
            ui["alerts"].extend(check_alerts(prev_status, frame["statuses"]))
            prev_status = frame["statuses"]
            wait_after_redraw(keys, ui, SERVERS, procs, started_at, history, monitor_only)
    except KeyboardInterrupt:
        pass
    finally:
        keys.close()
        if not monitor_only:
            stop_all(SERVERS, procs)


if __name__ == "__main__":
    main()
