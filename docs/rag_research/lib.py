#!/usr/bin/env python3
"""
Shared library for the RAG embedding research.

Provides:
  - corpus loading + cleaning (drop running headers/footers)
  - sentence segmentation with (page, line) provenance
  - token counting (tiktoken cl100k as a stable proxy tokenizer)
  - an embedding client for the llama.cpp OpenAI-compatible server,
    with an on-disk cache so re-runs are cheap and deterministic
  - cosine helpers
  - a tiny SQLite vector store (brute-force cosine; research scale)

The embedding model should return L2-normalized vectors,
so cosine == dot product.
"""
import hashlib
import json
import os
import re
import sqlite3
import struct
import sys
import time
from dataclasses import dataclass, asdict
from typing import Iterable

import numpy as np
import requests
import tiktoken

HERE = os.path.dirname(os.path.abspath(__file__))
DATA = os.path.join(HERE, "data")
CACHE_DB = os.path.join(DATA, "embed_cache.sqlite")

EMBED_URL = os.environ.get("EMBED_URL", "http://localhost:2235/v1/embeddings")
EMBED_MODEL = os.environ.get(
    "EMBED_MODEL", "granite-embedding-311M-multilingual-r2-Q8_0.gguf"
)
EMBED_DIM = 768
MODEL_CTX = 1536  # server n_ctx for this model

_ENC = tiktoken.get_encoding("cl100k_base")

# llama.cpp /tokenize endpoint -> exact token counts in the embedding model's
# own (granite) vocabulary. The server rejects inputs above its physical batch
# size (1500 tokens for this deployment), so chunk budgets MUST be expressed in
# these tokens, not in a proxy tokenizer's.
TOKENIZE_URL = os.environ.get("TOKENIZE_URL", "http://localhost:2235/tokenize")
MODEL_MAX_TOKENS = 1500  # server physical batch size == hard per-input limit

# Which embedding model the /tokenize endpoint is currently serving. Token counts
# are tokenizer-specific (granite/bert/qwen3/gemma/lfm2 all differ), so the gtok
# cache MUST be namespaced by model or one model would read another's counts.
# r3_build sets this to the active backend before chunking.
GTOK_MODEL = os.environ.get("GTOK_MODEL", "granite")


def n_tokens(text: str) -> int:
    """tiktoken cl100k proxy count (offline, no server needed)."""
    return len(_ENC.encode(text))


_GTOK_CONN: sqlite3.Connection | None = None


def _gtok_conn() -> sqlite3.Connection:
    global _GTOK_CONN
    if _GTOK_CONN is None:
        os.makedirs(DATA, exist_ok=True)
        _GTOK_CONN = sqlite3.connect(CACHE_DB)
        _GTOK_CONN.execute(
            "CREATE TABLE IF NOT EXISTS gtok (k TEXT PRIMARY KEY, n INTEGER)"
        )
    return _GTOK_CONN


def gtok(text: str) -> int:
    """Exact granite token count for `text` (cached, via llama.cpp /tokenize)."""
    conn = _gtok_conn()
    k = hashlib.sha256((GTOK_MODEL + "\x00" + text).encode("utf-8")).hexdigest()
    row = conn.execute("SELECT n FROM gtok WHERE k=?", (k,)).fetchone()
    if row is not None:
        return row[0]
    n = None
    for attempt in range(6):
        try:
            r = requests.post(TOKENIZE_URL, json={"content": text}, timeout=60)
            r.raise_for_status()
            n = len(r.json()["tokens"])
            break
        except Exception:  # noqa: BLE001 -- server may briefly restart
            if attempt == 5:
                raise
            time.sleep(2.0 * (attempt + 1))
    conn.execute("INSERT OR REPLACE INTO gtok (k,n) VALUES (?,?)", (k, n))
    conn.commit()
    return n


# Active token counter used for chunk budgeting and chunk stats. Defaults to the
# offline tiktoken proxy; build_indexes switches it to `gtok` so all caps are
# measured in real model tokens.
TOK = n_tokens


# --------------------------------------------------------------------------
# Corpus / sentence segmentation
# --------------------------------------------------------------------------

# Running header lines look like:
#   "C 202/22        EN        Official Journal of the European Union     7.6.2016"
#   "7.6.2016        EN        Official Journal of the European Union     C 202/23"
_HEADER_RE = re.compile(
    r"(Official Journal of the European Union|^\s*C\s*202/\d+|^\s*EN\s*$|^\s*\d+\.\d+\.\d{4}\s*$)"
)

# Abbreviations after which a period does NOT end a sentence.
_ABBREV = {
    "art", "arts", "no", "nos", "para", "paras", "pt", "pts", "p", "pp",
    "ch", "cf", "e.g", "i.e", "etc", "vol", "ec", "eu", "ets", "mr", "mrs",
    "ms", "dr", "ed", "eds", "al", "ibid", "cit", "op", "viz", "sec", "subpara",
}


def load_corpus(path: str | None = None) -> dict:
    path = path or os.path.join(DATA, "corpus.json")
    with open(path) as f:
        return json.load(f)


def _is_header(line: str) -> bool:
    return bool(_HEADER_RE.search(line))


@dataclass
class Sentence:
    id: int
    page: int
    line: int          # 0-based line index where the sentence starts
    text: str
    n_tok: int


def segment_corpus(corpus: dict) -> list[Sentence]:
    """
    Reconstruct sentences across hard line-wraps, keeping the (page, line)
    where each sentence begins. Running headers/footers are dropped.

    Strategy: walk the document building (char_offset -> (page,line)) anchors,
    join wrapped lines with spaces, treat blank lines as soft paragraph
    breaks, then run a regex sentence splitter and map sentence starts back
    to their originating (page, line).
    """
    buf: list[str] = []
    anchors: list[tuple[int, int, int]] = []  # (char_pos_in_buf, page, line)
    pos = 0
    for page in corpus["pages"]:
        pno = page["page"]
        for li, raw in enumerate(page["lines"]):
            line = raw.strip()
            if not line or _is_header(line):
                # paragraph break marker (helps the splitter)
                if buf and not buf[-1].endswith("\n"):
                    buf.append("\n")
                    pos += 1
                continue
            anchors.append((pos, pno, li))
            buf.append(line + " ")
            pos += len(line) + 1
    text = "".join(buf)

    spans = _split_sentences(text)
    anchor_pos = [a[0] for a in anchors]
    sents: list[Sentence] = []
    sid = 0
    import bisect
    for start, end in spans:
        s = text[start:end].strip()
        if len(s) < 3:
            continue
        # find the anchor at or before this sentence start
        idx = bisect.bisect_right(anchor_pos, start) - 1
        idx = max(0, idx)
        _, pno, li = anchors[idx]
        sents.append(Sentence(sid, pno, li, s, TOK(s)))
        sid += 1
    return sents


def _split_sentences(text: str) -> list[tuple[int, int]]:
    """Return (start,end) char spans of sentences. Legal-abbreviation aware."""
    spans = []
    start = 0
    n = len(text)
    i = 0
    while i < n:
        c = text[i]
        if c in ".!?":
            # look back for an abbreviation
            j = i - 1
            word_start = j
            while word_start >= 0 and (text[word_start].isalnum() or text[word_start] == "."):
                word_start -= 1
            word = text[word_start + 1:i].lower().rstrip(".")
            # lookahead: sentence boundary needs space + capital/quote/digit, or EOS
            k = i + 1
            while k < n and text[k] in " \n\t":
                k += 1
            nxt = text[k] if k < n else ""
            is_abbrev = word in _ABBREV
            single_initial = len(word) == 1 and word.isalpha()
            # a number like "1." starting a numbered paragraph is a boundary,
            # but "No. 5" is not (handled by abbrev 'no')
            if c == "." and (is_abbrev or single_initial):
                i += 1
                continue
            if nxt and not (nxt.isupper() or nxt.isdigit() or nxt in "‘“\"'("):
                i += 1
                continue
            spans.append((start, i + 1))
            start = k
            i = k
            continue
        i += 1
    if start < n and text[start:].strip():
        spans.append((start, n))
    return spans


# --------------------------------------------------------------------------
# Embedding client with on-disk cache
# --------------------------------------------------------------------------

def _cache_conn() -> sqlite3.Connection:
    os.makedirs(DATA, exist_ok=True)
    c = sqlite3.connect(CACHE_DB)
    c.execute(
        "CREATE TABLE IF NOT EXISTS emb (k TEXT PRIMARY KEY, v BLOB NOT NULL)"
    )
    return c


def pack(vec: np.ndarray) -> bytes:
    return np.asarray(vec, dtype=np.float32).tobytes()


def unpack(blob: bytes) -> np.ndarray:
    return np.frombuffer(blob, dtype=np.float32)


# Registry of embedding backends. Each entry: url, model id, dim, and the
# instruction prefixes the model expects for queries vs documents. granite has
# no prefixes; Qwen3-Embedding uses an "Instruct:/Query:" prefix for queries.
# The single-embedder study (paper3): five candidate models, each served in turn
# on :2235 by r3_server (one model resident at a time, doing boundaries + chunk
# vectors + query vectors). Two prompt modes per model:
#   <name>      native prompt convention (the model's documented query/doc prompt)
#   <name>_np   no-prefix / raw text both sides == how Harlequin embeds today
# granite is symmetric (no native prompt), so granite == granite_np.
# Doc embeddings are shared across modes when doc_prefix is identical, so the
# on-disk cache (keyed by model+prefixed-text) dedupes them automatically.
_U = "http://localhost:2235/v1/embeddings"
_QWEN_Q = ("Instruct: Given a web search query, retrieve relevant passages that "
           "answer the query\nQuery: ")
EMBEDDERS = {
    # --- native prompt convention -----------------------------------------
    "granite": dict(url=_U, model="granite-embedding-311M-multilingual-r2-Q8_0.gguf",
                    dim=768, query_prefix="", doc_prefix="", max_batch_tokens=2200),
    "snowflake": dict(url=_U, model="snowflake-arctic-embed-l-v2.0-q8_0.gguf",
                      dim=1024, query_prefix="query: ", doc_prefix="", max_batch_tokens=2200),
    "qwen06b": dict(url=_U, model="Qwen3-Embedding-0.6B-Q8_0.gguf",
                    dim=1024, query_prefix=_QWEN_Q, doc_prefix="", max_batch_tokens=2200),
    "gemma": dict(url=_U, model="embeddinggemma-300M-Q8_0.gguf",
                  dim=768, query_prefix="task: search result | query: ",
                  doc_prefix="title: none | text: ", max_batch_tokens=1800),
    "lfm2": dict(url=_U, model="LFM2.5-Embedding-350M-Q8_0.gguf",
                 dim=1024, query_prefix="query: ", doc_prefix="document: ", max_batch_tokens=2200),
    # --- no-prefix (raw text both sides, == Harlequin today) --------------
    "snowflake_np": dict(url=_U, model="snowflake-arctic-embed-l-v2.0-q8_0.gguf",
                         dim=1024, query_prefix="", doc_prefix="", max_batch_tokens=2200),
    "qwen06b_np": dict(url=_U, model="Qwen3-Embedding-0.6B-Q8_0.gguf",
                       dim=1024, query_prefix="", doc_prefix="", max_batch_tokens=2200),
    "gemma_np": dict(url=_U, model="embeddinggemma-300M-Q8_0.gguf",
                     dim=768, query_prefix="", doc_prefix="", max_batch_tokens=1800),
    "lfm2_np": dict(url=_U, model="LFM2.5-Embedding-350M-Q8_0.gguf",
                    dim=1024, query_prefix="", doc_prefix="", max_batch_tokens=2200),
}

# The 5 physical models, and which r3_server backend serves each. Multiple
# EMBEDDERS configs (native/_np) share one resident server.
MODELS = ["granite", "snowflake", "qwen06b", "gemma", "lfm2"]
SERVER_OF = {  # EMBEDDERS config -> r3_server backend (which gguf to load)
    "granite": "granite",
    "snowflake": "snowflake", "snowflake_np": "snowflake",
    "qwen06b": "qwen06b", "qwen06b_np": "qwen06b",
    "gemma": "gemma", "gemma_np": "gemma",
    "lfm2": "lfm2", "lfm2_np": "lfm2",
}
# Prompt modes to evaluate per model for the prefix-gap chapter. granite has no
# native prompt so only one mode.
MODES_OF = {
    "granite": ["granite"],
    "snowflake": ["snowflake", "snowflake_np"],
    "qwen06b": ["qwen06b", "qwen06b_np"],
    "gemma": ["gemma", "gemma_np"],
    "lfm2": ["lfm2", "lfm2_np"],
}


class Embedder:
    """Batched, cached embedding client. Multi-model + instruction prefixes.

    `embed(texts, role=...)` applies the query or document prefix for the chosen
    backend; the cache is keyed by (model, prefixed-text) so different models and
    roles never collide. Defaults reproduce the original granite behaviour.
    """

    def __init__(self, backend: str = "granite", batch_size: int = 32,
                 verbose: bool = True, workers: int = 1):
        cfg = EMBEDDERS[backend]
        self.backend = backend
        self.url = cfg["url"]
        self.model = cfg["model"]
        self.dim = cfg["dim"]
        self.query_prefix = cfg["query_prefix"]
        self.doc_prefix = cfg["doc_prefix"]
        self.max_batch_tokens = cfg.get("max_batch_tokens")
        self.batch_size = batch_size
        # Concurrency note: although llama-server advertises n_parallel=4 slots,
        # firing >1 concurrent /v1/embeddings request makes this build abort in
        # server_context_impl::decode (ggml_abort) — the combined parallel-sequence
        # batch overflows the physical batch. So embedding is SEQUENTIAL by default
        # (workers=1). Raising workers is opt-in and known to crash the server.
        self.workers = workers
        self.verbose = verbose
        self.conn = _cache_conn()
        self.calls = 0
        self.cache_hits = 0
        self.tokens = 0

    def _key(self, text: str) -> str:
        h = hashlib.sha256()
        h.update(self.model.encode())
        h.update(b"\x00")
        h.update(text.encode("utf-8"))
        return h.hexdigest()

    def _post(self, inputs: list[str]) -> list[np.ndarray]:
        for attempt in range(5):
            try:
                r = requests.post(
                    self.url,
                    json={"model": self.model, "input": inputs},
                    timeout=300,
                )
                r.raise_for_status()
                d = r.json()
                self.tokens += d.get("usage", {}).get("total_tokens", 0)
                self.calls += 1
                return [np.asarray(e["embedding"], dtype=np.float32) for e in d["data"]]
            except Exception as ex:  # noqa: BLE001
                if attempt == 4:
                    raise
                if self.verbose:
                    print(f"  embed retry {attempt+1}: {ex}", file=sys.stderr)
                time.sleep(1.5 * (attempt + 1))
        raise RuntimeError("unreachable")

    def embed(self, texts: list[str], role: str = "doc") -> np.ndarray:
        """Return (len(texts), dim) float32 array, using cache.

        role: "doc" or "query" — selects the instruction prefix.
        """
        prefix = self.query_prefix if role == "query" else self.doc_prefix
        texts = [prefix + t for t in texts]
        out: list[np.ndarray | None] = [None] * len(texts)
        todo: list[int] = []
        keys = [self._key(t) for t in texts]
        cur = self.conn.cursor()
        for i, k in enumerate(keys):
            row = cur.execute("SELECT v FROM emb WHERE k=?", (k,)).fetchone()
            if row is not None:
                out[i] = unpack(row[0])
                self.cache_hits += 1
            else:
                todo.append(i)
        # batch the misses. If max_batch_tokens is set (small-context embedding
        # servers), pack each request by estimated token budget so a batch of
        # large chunks never overflows the server micro-batch (large chunks go
        # solo). Otherwise batch by fixed count. Batches are POSTed concurrently
        # (up to self.workers) to saturate the server's parallel slots; cache
        # writes happen back on this thread as results arrive (sqlite is not
        # shared across threads).
        batches = self._make_batches(todo, texts)
        done = 0
        from concurrent.futures import ThreadPoolExecutor
        work = lambda idxs: (idxs, self._post([texts[i] for i in idxs]))
        with ThreadPoolExecutor(max_workers=max(1, self.workers)) as ex:
            for idxs, vecs in ex.map(work, batches):
                for i, v in zip(idxs, vecs):
                    out[i] = v
                    self.conn.execute(
                        "INSERT OR REPLACE INTO emb (k,v) VALUES (?,?)",
                        (keys[i], pack(v)),
                    )
                self.conn.commit()
                done += len(idxs)
                if self.verbose and todo:
                    print(f"    embedded {done}/{len(todo)} new", file=sys.stderr)
        return np.vstack([o for o in out])  # type: ignore[arg-type]

    @staticmethod
    def _est_tokens(text: str) -> int:
        return len(text) // 3 + 1          # conservative chars->tokens proxy

    def _make_batches(self, todo: list[int], texts: list[str]) -> list[list[int]]:
        if not self.max_batch_tokens:
            return [todo[b:b + self.batch_size]
                    for b in range(0, len(todo), self.batch_size)]
        batches, cur, cur_tok = [], [], 0
        for i in todo:
            t = self._est_tokens(texts[i])
            if cur and (len(cur) >= self.batch_size or cur_tok + t > self.max_batch_tokens):
                batches.append(cur); cur, cur_tok = [], 0
            cur.append(i); cur_tok += t
        if cur:
            batches.append(cur)
        return batches

    def embed_one(self, text: str) -> np.ndarray:
        return self.embed([text])[0]


def cosine(a: np.ndarray, b: np.ndarray) -> float:
    return float(np.dot(a, b) / (np.linalg.norm(a) * np.linalg.norm(b) + 1e-12))


# --------------------------------------------------------------------------
# Chat LLM client (Qwen3.6-35B via llama.cpp), cached, reasoning disabled
# --------------------------------------------------------------------------

CHAT_URL = os.environ.get("CHAT_URL", "http://localhost:2234/v1/chat/completions")
# The chat server (started via ~/LLM/llm_for_hermes_qwen.sh) serves
# Qwen3.6-35B-A3B. llama-server ignores the request "model" field, so this only
# namespaces the response cache.
CHAT_MODEL = os.environ.get("CHAT_MODEL", "qwen3.6-35b-hermes")


class ChatLLM:
    """Cached chat client. Thinking/reasoning is disabled for speed and
    determinism (Qwen3 emits reasoning tokens otherwise). Responses are cached
    in the shared sqlite db keyed by (model, prompt, system, temperature)."""

    def __init__(self, temperature: float = 0.0, verbose: bool = False):
        self.temperature = temperature
        self.verbose = verbose
        self.conn = _cache_conn()
        self.conn.execute(
            "CREATE TABLE IF NOT EXISTS chat (k TEXT PRIMARY KEY, v TEXT NOT NULL)"
        )
        self.calls = 0
        self.cache_hits = 0

    def _key(self, system: str, prompt: str, max_tokens: int) -> str:
        h = hashlib.sha256()
        for p in (CHAT_MODEL, str(self.temperature), str(max_tokens), system, prompt):
            h.update(p.encode("utf-8"))
            h.update(b"\x00")
        return h.hexdigest()

    def ask(self, prompt: str, system: str = "", max_tokens: int = 512) -> str:
        k = self._key(system, prompt, max_tokens)
        row = self.conn.execute("SELECT v FROM chat WHERE k=?", (k,)).fetchone()
        if row is not None:
            self.cache_hits += 1
            return row[0]
        msgs = []
        if system:
            msgs.append({"role": "system", "content": system})
        msgs.append({"role": "user", "content": prompt + " /no_think"})
        # Disable reasoning per-request (Qwen3): chain-of-thought would be far
        # too slow on CPU for thousands of calls and we don't need it for these
        # extraction/judgement tasks. The final answer is in message.content.
        body = {
            "model": CHAT_MODEL, "messages": msgs,
            "max_tokens": max_tokens, "temperature": self.temperature,
            "chat_template_kwargs": {"enable_thinking": False},
        }
        for attempt in range(5):
            try:
                r = requests.post(CHAT_URL, json=body, timeout=600)
                r.raise_for_status()
                txt = (r.json()["choices"][0]["message"]["content"] or "").strip()
                # strip any stray <think></think> remnants
                txt = re.sub(r"<think>.*?</think>", "", txt, flags=re.DOTALL).strip()
                self.conn.execute(
                    "INSERT OR REPLACE INTO chat (k,v) VALUES (?,?)", (k, txt))
                self.conn.commit()
                self.calls += 1
                return txt
            except Exception as ex:  # noqa: BLE001
                if attempt == 4:
                    raise
                if self.verbose:
                    print(f"  chat retry {attempt+1}: {ex}", file=sys.stderr)
                time.sleep(2.0 * (attempt + 1))
        raise RuntimeError("unreachable")


# --------------------------------------------------------------------------
# SQLite vector store (sqlite-vec / vec0)
# --------------------------------------------------------------------------

# Loadable sqlite-vec extension, compiled from the asg017/sqlite-vec source
# (see README). Stored next to this file as vec0.so.
VEC_EXT = os.environ.get("VEC_EXT", os.path.join(HERE, "vec0"))


def open_db(path: str) -> sqlite3.Connection:
    """Open a SQLite connection with the sqlite-vec extension loaded."""
    conn = sqlite3.connect(path)
    conn.enable_load_extension(True)
    conn.load_extension(VEC_EXT)
    conn.enable_load_extension(False)
    return conn


class VectorStore:
    """
    One SQLite DB per index variant. Vectors live in a sqlite-vec `vec0`
    virtual table declared with `distance_metric=cosine`; KNN search is done
    by sqlite-vec itself (`embedding MATCH ? AND k=?`). A companion `chunks`
    table holds the text + sentence-span provenance, joined on rowid==id.
    """

    def __init__(self, path: str, dim: int = EMBED_DIM):
        self.path = path
        self.dim = dim
        self.conn = open_db(path)
        self.conn.execute(
            """CREATE TABLE IF NOT EXISTS chunks (
                   id INTEGER PRIMARY KEY,
                   text TEXT NOT NULL,
                   first_page INTEGER NOT NULL,
                   first_line INTEGER NOT NULL,
                   last_page INTEGER NOT NULL,
                   last_line INTEGER NOT NULL,
                   sent_start INTEGER NOT NULL,
                   sent_end INTEGER NOT NULL,
                   pages TEXT NOT NULL,
                   n_tok INTEGER NOT NULL
               )"""
        )
        self.conn.execute(
            f"CREATE VIRTUAL TABLE IF NOT EXISTS vec_idx USING "
            f"vec0(embedding float[{dim}] distance_metric=cosine)"
        )
        self.conn.execute(
            "CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT)"
        )

    def reset(self):
        self.conn.execute("DELETE FROM chunks")
        self.conn.execute("DELETE FROM vec_idx")
        self.conn.execute("DELETE FROM meta")
        self.conn.commit()

    def set_meta(self, **kw):
        for k, v in kw.items():
            self.conn.execute(
                "INSERT OR REPLACE INTO meta (k,v) VALUES (?,?)",
                (k, json.dumps(v)),
            )
        self.conn.commit()

    def get_meta(self) -> dict:
        rows = self.conn.execute("SELECT k,v FROM meta").fetchall()
        return {k: json.loads(v) for k, v in rows}

    def add(self, rows: list[dict], vecs: np.ndarray):
        for i, (r, v) in enumerate(zip(rows, vecs), start=1):
            cid = self.conn.execute(
                "INSERT INTO chunks "
                "(text,first_page,first_line,last_page,last_line,"
                " sent_start,sent_end,pages,n_tok) "
                "VALUES (?,?,?,?,?,?,?,?,?)",
                (r["text"], r["first_page"], r["first_line"],
                 r["last_page"], r["last_line"], r["sent_start"], r["sent_end"],
                 json.dumps(r["pages"]), r["n_tok"]),
            ).lastrowid
            self.conn.execute(
                "INSERT INTO vec_idx (rowid, embedding) VALUES (?, ?)",
                (cid, pack(v)),
            )
        self.conn.commit()

    def count(self) -> int:
        return self.conn.execute("SELECT COUNT(*) FROM chunks").fetchone()[0]

    def search(self, qvec: np.ndarray, k: int = 10) -> list[tuple[int, float]]:
        """KNN via sqlite-vec; returns (chunk_id, cosine_similarity)."""
        rows = self.conn.execute(
            "SELECT rowid, distance FROM vec_idx "
            "WHERE embedding MATCH ? AND k = ? ORDER BY distance",
            (pack(qvec), k),
        ).fetchall()
        # vec0 cosine distance = 1 - cosine_similarity
        return [(int(rid), 1.0 - float(dist)) for rid, dist in rows]

    def get_chunk(self, cid: int) -> dict:
        r = self.conn.execute(
            "SELECT id,text,first_page,first_line,last_page,last_line,"
            "sent_start,sent_end,pages,n_tok FROM chunks WHERE id=?",
            (cid,),
        ).fetchone()
        return {
            "id": r[0], "text": r[1], "first_page": r[2], "first_line": r[3],
            "last_page": r[4], "last_line": r[5], "sent_start": r[6],
            "sent_end": r[7], "pages": json.loads(r[8]), "n_tok": r[9],
        }

    def all_chunks(self) -> list[dict]:
        ids = self.conn.execute("SELECT id FROM chunks ORDER BY id").fetchall()
        return [self.get_chunk(i[0]) for i in ids]
