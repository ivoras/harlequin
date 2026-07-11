#!/usr/bin/env python3
"""Scaled cross-lingual check: 250 EN corpus sentences translated to Croatian
by the chat model, then HR->EN retrieval over the 250x250 cosine matrix for
qwen3-embedding-4b vs mistral-embed-2312."""
import json, os, random, re, sys, time
import numpy as np
import requests

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from lib import load_corpus, segment_corpus  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
KEY = os.environ["OPENROUTER_API_KEY"]
EMB_URL = "https://openrouter.ai/api/v1/embeddings"
CHAT_URL = "https://openrouter.ai/api/v1/chat/completions"
N = 250
SEED = 11
HR_FILE = os.path.join(HERE, "hr_250.json")

QWEN_Q = ("Instruct: Given a web search query, retrieve relevant passages that "
          "answer the query\nQuery: ")
MODELS = {
    "qwen3-emb-4b": dict(model="qwen/qwen3-embedding-4b", query_prefix=QWEN_Q),
    "mistral-embed": dict(model="mistralai/mistral-embed-2312", query_prefix=""),
}


def pick_sentences():
    sents = [s.text.replace("\n", " ") for s in
             segment_corpus(load_corpus(
                 os.path.join(os.path.dirname(os.path.abspath(__file__)), "data", "corpus.json")))]
    random.seed(SEED)
    clean = [s for s in sents if 70 < len(s) < 220 and ". . ." not in s
             and not s.startswith(("Declaration", "Protocol", "CHAPTER", "TITLE"))]
    return random.sample(clean, N)


def chat(prompt):
    for attempt in range(5):
        try:
            r = requests.post(CHAT_URL, headers={"Authorization": f"Bearer {KEY}"},
                              json={"model": "deepseek/deepseek-v4-flash",
                                    "temperature": 0.1,
                                    "messages": [{"role": "user", "content": prompt}]},
                              timeout=600)
            r.raise_for_status()
            return r.json()["choices"][0]["message"]["content"]
        except Exception as e:
            if attempt == 4:
                raise
            print(f"chat retry ({e})", file=sys.stderr)
            time.sleep(3 * (attempt + 1))


def translate(en):
    if os.path.exists(HR_FILE):
        hr = json.load(open(HR_FILE))
        if len(hr) == len(en):
            return hr
    hr = []
    B = 25
    for i in range(0, len(en), B):
        batch = en[i:i + B]
        numbered = "\n".join(f"{j+1}. {s}" for j, s in enumerate(batch))
        prompt = (
            "Translate the following numbered English sentences (EU treaty text) "
            "into Croatian, using standard Croatian EU legal terminology. "
            "Reply with ONLY the numbered Croatian translations, one per line, "
            "same numbering, no extra commentary.\n\n" + numbered)
        for attempt in range(3):
            text = chat(prompt)
            lines = {}
            for line in text.strip().splitlines():
                m = re.match(r"\s*(\d+)[.)]\s*(.+)", line.strip())
                if m:
                    lines[int(m.group(1))] = m.group(2).strip()
            if all(j + 1 in lines for j in range(len(batch))):
                hr.extend(lines[j + 1] for j in range(len(batch)))
                break
            print(f"  translate batch {i//B}: got {len(lines)}/{len(batch)}, retrying",
                  file=sys.stderr)
        else:
            raise RuntimeError(f"translation batch {i//B} failed")
        print(f"  translated {len(hr)}/{len(en)}", file=sys.stderr)
    json.dump(hr, open(HR_FILE, "w"), ensure_ascii=False, indent=1)
    return hr


def embed(model, texts):
    out = []
    for i in range(0, len(texts), 64):
        batch = texts[i:i + 64]
        for attempt in range(5):
            try:
                r = requests.post(EMB_URL, headers={"Authorization": f"Bearer {KEY}"},
                                  json={"model": model, "input": batch}, timeout=300)
                r.raise_for_status()
                d = sorted(r.json()["data"], key=lambda x: x["index"])
                out.extend(np.asarray(x["embedding"], dtype=np.float32) for x in d)
                break
            except Exception as e:
                if attempt == 4:
                    raise
                print(f"embed retry ({e})", file=sys.stderr)
                time.sleep(2 * (attempt + 1))
    v = np.vstack(out)
    return v / np.linalg.norm(v, axis=1, keepdims=True)


def main():
    en = pick_sentences()
    hr = translate(en)
    n = len(en)
    print(f"{n} EN/HR pairs (LLM-translated); HR = query, EN = document\n")
    print(f"{'model':<15} {'top1':>6} {'top5':>6} {'diag¯':>7} {'offdiag¯':>9} "
          f"{'margin¯':>8} {'p10-margin':>11} {'neg-margins':>12}")
    for name, cfg in MODELS.items():
        dv = embed(cfg["model"], en)
        qv = embed(cfg["model"], [cfg["query_prefix"] + t for t in hr])
        sims = qv @ dv.T
        diag = np.diag(sims)
        off = sims[~np.eye(n, dtype=bool)]
        order = np.argsort(-sims, axis=1)
        rank = np.array([np.where(order[i] == i)[0][0] for i in range(n)])
        rival = np.where(np.eye(n, dtype=bool), -np.inf, sims).max(axis=1)
        margin = diag - rival
        print(f"{name:<15} {(rank == 0).mean():>6.3f} {(rank < 5).mean():>6.3f} "
              f"{diag.mean():>7.3f} {off.mean():>9.3f} {margin.mean():>8.3f} "
              f"{np.percentile(margin, 10):>11.3f} {(margin < 0).sum():>12d}")


if __name__ == "__main__":
    main()
