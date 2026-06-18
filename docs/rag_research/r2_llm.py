#!/usr/bin/env python3
"""
LLM-in-the-loop retrieval components (require the chat server on :2234):

  hyde_vectors(backend)      HyDE: generate a hypothetical answer per query and
                             embed it as the query vector.
  llm_rerank(...)            listwise rerank of a candidate list via one chat call.
  build_contextual_index()   Contextual Retrieval: prepend an LLM-written context
                             sentence to each (small) chunk before embedding.
  gen_eval(...)              generation-grounded metric: feed retrieved context to
                             the LLM, judge whether it answers the question.

All chat calls are cached (lib.ChatLLM), so reruns are cheap.
"""
import json
import os
import re

import numpy as np

import chunkers
from lib import DATA, ChatLLM, Embedder, VectorStore, load_corpus, segment_corpus
import lib
from r2_retrieval import Retriever, idx_path, rrf

DOC_DESC = ("the consolidated Treaty on European Union (TEU) with its protocols "
            "and declarations")


# --------------------------------------------------------------- HyDE
def hyde_vectors(backend: str, llm: ChatLLM | None = None) -> dict:
    """query_id -> embedding of an LLM-generated hypothetical answer."""
    llm = llm or ChatLLM()
    emb = Embedder(backend, verbose=False)
    qs = json.load(open(os.path.join(DATA, "eval_questions.json")))["questions"]
    hypo = []
    for q in qs:
        a = llm.ask(
            f"Write one or two sentences that would plausibly appear in {DOC_DESC} "
            f"and answer this question. Output only the sentence(s).\n\nQuestion: {q['q']}",
            max_tokens=120)
        hypo.append(a if a else q["q"])
    V = emb.embed(hypo, role="doc")            # embed hypotheticals as documents
    out = {q["id"]: V[i] for i, q in enumerate(qs)}
    out["_emb"] = emb
    return out


# --------------------------------------------------------------- LLM rerank
def llm_rerank(query: str, cand: list[tuple[int, float]], by_id: dict,
               llm: ChatLLM, k: int = 10) -> list[tuple[int, float]]:
    cand = cand[:k]
    if len(cand) <= 1:
        return cand
    lines = []
    for i, (cid, _s) in enumerate(cand):
        txt = " ".join(by_id[cid]["text"].split())[:300]
        lines.append(f"[{i}] {txt}")
    prompt = (f"Question: {query}\n\nPassages:\n" + "\n".join(lines) +
              f"\n\nReturn the passage indices (0-{len(cand)-1}) ordered from most "
              "to least relevant to the question, as a comma-separated list. Indices only.")
    ans = llm.ask(prompt, max_tokens=80)
    order = [int(x) for x in re.findall(r"\d+", ans) if int(x) < len(cand)]
    seen, final = set(), []
    for i in order:
        if i not in seen:
            seen.add(i); final.append(cand[i])
    for i in range(len(cand)):           # append any the LLM dropped
        if i not in seen:
            final.append(cand[i])
    return [(cid, 1.0 / r) for r, (cid, _s) in enumerate(final, start=1)]


# --------------------------------------------------------------- Contextual Retrieval
def build_contextual_index(base_variant: str, backend: str = "granite",
                           llm: ChatLLM | None = None) -> str:
    """Prepend an LLM-generated situating context to each chunk, then embed.
    Context source is the chunk's enclosing structural (Article) chunk, kept
    cheap. Writes idx/{backend}/ctx_{base_variant}.sqlite."""
    llm = llm or ChatLLM()
    lib.TOK = lib.gtok
    sents = segment_corpus(load_corpus())
    # parent structural chunks for context
    parents = chunkers.structure(sents, 1024)
    def parent_text(sent_id):
        for p in parents:
            if p["sent_start"] <= sent_id <= p["sent_end"]:
                return p["text"]
        return ""
    base = Retriever(backend, base_variant, with_bm25=False)
    emb = Embedder(backend, verbose=True)
    ctx_texts = []
    for c in base.chunks:
        ctx_src = " ".join(parent_text(c["sent_start"]).split())[:1500]
        chunk_txt = " ".join(c["text"].split())[:600]
        ctx = llm.ask(
            f"Here is a section of {DOC_DESC}:\n<section>{ctx_src}</section>\n\n"
            f"Here is a chunk from it:\n<chunk>{chunk_txt}</chunk>\n\n"
            "Give a short (max 25 words) context situating this chunk within the "
            "document so it can be found by search. Output only the context.",
            max_tokens=60)
        ctx_texts.append((ctx + " " + c["text"]) if ctx else c["text"])
    V = emb.embed(ctx_texts, role="doc")
    name = f"ctx_{base_variant}"
    store = VectorStore(idx_path(backend, name), dim=emb.dim)
    store.reset(); store.set_meta(name=name, backend=backend, n_chunks=len(base.chunks))
    store.add(base.chunks, V)            # keep original spans for scoring
    return name


# --------------------------------------------------------------- generation eval
def gen_eval(label, backend, variant, mode, qvecs, llm, subset_ids, k=5):
    """LLM-judge: does the top-k retrieved context let the model answer? Scored
    against the grounded support sentence. Returns fraction correct on subset."""
    r = Retriever(backend, variant, embedder=qvecs.get("_emb"), with_bm25=(mode != "dense"))
    qs = {q["id"]: q for q in json.load(open(os.path.join(DATA, "eval_questions.json")))["questions"]}
    correct = []
    for qid in subset_ids:
        q = qs[qid]
        if mode == "dense":
            ranked = r.dense(qvecs[qid], k)
        elif mode == "hybrid":
            ranked = r.hybrid(qvecs[qid], q["q"], k=k, depth=50)
        else:
            ranked = r.lexical(q["q"], k)
        ctx = "\n---\n".join(" ".join(r.by_id[cid]["text"].split()) for cid, _ in ranked[:k])
        ans = llm.ask(
            f"Context:\n{ctx}\n\nQuestion: {q['q']}\n\nAnswer using only the context "
            "in one short sentence. If the context does not contain the answer, reply NO_ANSWER.",
            max_tokens=80)
        gold = q["expected_text"]
        verdict = llm.ask(
            f"Question: {q['q']}\nReference answer: {gold}\nModel answer: {ans}\n\n"
            "Does the model answer match the reference (same fact)? Reply YES or NO.",
            max_tokens=5)
        correct.append(1.0 if verdict.strip().upper().startswith("Y") else 0.0)
    return float(np.mean(correct)) if correct else 0.0
