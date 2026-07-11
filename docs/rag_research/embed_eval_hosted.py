#!/usr/bin/env python3
"""Retrieval-quality comparison: current embed model (Qwen3-Embedding-0.6B,
local, production prefixes) vs google/gemini-embedding-001 (OpenRouter).

Uses the rag_research eval set: 2112 TEU/TFEU sentences, questions with gold
support-sentence ids (answer_sets.json). Metric: sentence retrieval by plain
cosine — hit@1/@5/@10 and MRR@10 over a seeded sample of questions.
"""
import json, os, random, sys, time
import numpy as np
import requests

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from lib import load_corpus, segment_corpus  # noqa: E402

DATA = os.path.join(os.path.dirname(os.path.abspath(__file__)), "data")
CACHE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "data", "vec_cache_hosted")
os.makedirs(CACHE, exist_ok=True)

N_QUESTIONS = 150
SEED = 42

MODELS = {
    "qwen06b(current)": dict(
        url="http://localhost:2235/v1/embeddings",
        model="Qwen3-Embedding-0.6B",
        key="", query_prefix="query: ", doc_prefix="", batch=64),
    "gemini-emb-001": dict(
        url="https://openrouter.ai/api/v1/embeddings",
        model="google/gemini-embedding-001",
        key=os.environ["OPENROUTER_API_KEY"], query_prefix="", doc_prefix="", batch=64),
    "mistral-embed": dict(
        url="https://openrouter.ai/api/v1/embeddings",
        model="mistralai/mistral-embed-2312",
        key=os.environ["OPENROUTER_API_KEY"], query_prefix="", doc_prefix="", batch=64),
    "qwen3-emb-4b": dict(
        url="https://openrouter.ai/api/v1/embeddings",
        model="qwen/qwen3-embedding-4b",
        key=os.environ["OPENROUTER_API_KEY"],
        # Same instruct convention as the local 0.6B sibling; hosted endpoints
        # don't apply it server-side, so send it explicitly.
        query_prefix=("Instruct: Given a web search query, retrieve relevant "
                      "passages that answer the query\nQuery: "),
        doc_prefix="", batch=64),
    "openai-3-large": dict(
        url="https://openrouter.ai/api/v1/embeddings",
        model="openai/text-embedding-3-large",
        key=os.environ["OPENROUTER_API_KEY"], query_prefix="", doc_prefix="", batch=64),
    # granite (the RAG-study runner-up) is read straight from the study's
    # embedding cache — the model isn't currently being served locally.
    "granite(cache)": dict(
        cache_model="granite-embedding-311M-multilingual-r2-Q8_0.gguf",
        query_prefix="", doc_prefix=""),
}

RESEARCH_CACHE = os.path.join(DATA, "embed_cache.sqlite")


def embed_from_cache(cfg, texts, prefix):
    import hashlib, sqlite3
    conn = sqlite3.connect(RESEARCH_CACHE)
    out = []
    for t in texts:
        h = hashlib.sha256()
        h.update(cfg["cache_model"].encode())
        h.update(b"\x00")
        h.update((prefix + t).encode())
        row = conn.execute("SELECT v FROM emb WHERE k=?", (h.hexdigest(),)).fetchone()
        if row is None:
            raise KeyError(f"not in research cache: {t[:60]!r}")
        out.append(np.frombuffer(row[0], dtype=np.float32))
    return np.vstack(out)


def embed_all(name, cfg, texts, role):
    fn = os.path.join(CACHE, f"{name.replace('/', '_')}.{role}.npy")
    if os.path.exists(fn):
        v = np.load(fn)
        if len(v) == len(texts):
            return v
    prefix = cfg["query_prefix"] if role == "q" else cfg["doc_prefix"]
    if "cache_model" in cfg:
        return embed_from_cache(cfg, texts, prefix)
    out = []
    headers = {"Authorization": f"Bearer {cfg['key']}"} if cfg["key"] else {}
    for i in range(0, len(texts), cfg["batch"]):
        batch = [prefix + t for t in texts[i:i + cfg["batch"]]]
        for attempt in range(5):
            try:
                r = requests.post(cfg["url"], headers=headers, timeout=300,
                                  json={"model": cfg["model"], "input": batch})
                r.raise_for_status()
                d = r.json()["data"]
                d.sort(key=lambda x: x["index"])
                out.extend(np.asarray(x["embedding"], dtype=np.float32) for x in d)
                break
            except Exception as e:
                if attempt == 4:
                    raise
                print(f"  retry {attempt+1} ({e})", file=sys.stderr)
                time.sleep(2 * (attempt + 1))
        print(f"  {name} {role}: {min(i+cfg['batch'], len(texts))}/{len(texts)}",
              end="\r", file=sys.stderr)
    print(file=sys.stderr)
    v = np.vstack(out)
    np.save(fn, v)
    return v


def main():
    corpus = load_corpus(os.path.join(DATA, "corpus.json"))
    sents = [s.text for s in segment_corpus(corpus)]
    answers = {int(k): set(v) for k, v in
               json.load(open(os.path.join(DATA, "answer_sets.json"))).items()}
    questions = {q["id"]: q["q"] for q in
                 json.load(open(os.path.join(DATA, "eval_questions.json")))["questions"]}
    qids = sorted(qid for qid in answers if qid in questions and answers[qid])
    random.seed(SEED)
    sample = random.sample(qids, min(N_QUESTIONS, len(qids)))
    qtexts = [questions[qid] for qid in sample]
    print(f"{len(sents)} sentences, {len(sample)} sampled questions\n")

    print(f"{'model':<20} {'dim':>5} {'hit@1':>7} {'hit@5':>7} {'hit@10':>7} {'MRR@10':>7}")
    for name, cfg in MODELS.items():
        dv = embed_all(name, cfg, sents, "d")
        qv = embed_all(name, cfg, qtexts, "q")
        dv = dv / np.linalg.norm(dv, axis=1, keepdims=True)
        qv = qv / np.linalg.norm(qv, axis=1, keepdims=True)
        sims = qv @ dv.T  # [nq, nsent]
        h1 = h5 = h10 = 0
        mrr = 0.0
        for row, qid in enumerate(sample):
            gold = answers[qid]
            top = np.argsort(-sims[row])[:10]
            rank = next((i for i, sid in enumerate(top) if sid in gold), None)
            if rank is not None:
                mrr += 1.0 / (rank + 1)
                h10 += 1
                if rank < 5:
                    h5 += 1
                if rank == 0:
                    h1 += 1
        n = len(sample)
        print(f"{name:<20} {dv.shape[1]:>5} {h1/n:>7.3f} {h5/n:>7.3f} {h10/n:>7.3f} {mrr/n:>7.3f}")


if __name__ == "__main__":
    main()
