#!/usr/bin/env python3
"""
Build EXACT / EXACT-ish questions to de-paraphrase the RAG eval set.

  mine   : 250 grounded queries anchored on exact tokens mined from the PDF
           (percentages, protocol numbers, article(+paragraph) refs, dates,
           grouped figures). Deterministic, no LLM. -> data/exact_mined.json
  merge  : append data/exact_mined.json + data/exact_llm.json into
           data/eval_questions.json and data/answer_sets.json (new ids), updating
           the header counts. Idempotent: it strips any previously merged
           category in {exact, exact_llm} first.

Grounding: a query's acceptable answer set is every sentence containing its exact
token/phrase; support_sent is the first such sentence. Query text = the exact
token plus a few salient content words from that sentence (dates use a natural
'what happened on <date>' phrasing).
"""
import json
import os
import re
import sys
from collections import defaultdict

from lib import DATA, load_corpus, segment_corpus

STOP = set("the a an of to in and or for on by with as at is are be shall this that "
           "which it its from not no any all may than have has was were will would "
           "such other into under upon their they them then there where when who whom "
           "these those been being if but also each per via both more most".split())

MONTHS = "January|February|March|April|May|June|July|August|September|October|November|December"
RE_PERCENT = re.compile(r"\b\d{1,3}\s*%")
RE_ARTPARA = re.compile(r"\bArticle\s+\d+[a-z]?\(\d+\)")
RE_ARTICLE = re.compile(r"\bArticle\s+\d+[a-z]?\b")
RE_PROTO = re.compile(r"\bProtocol\s+\(?No\s*\d+\)?")
RE_DATE = re.compile(r"\b\d{1,2}\s+(?:%s)\s+\d{4}\b" % MONTHS)
RE_FIGURE = re.compile(r"\b\d{1,3}(?:\s\d{3})+\b")        # grouped thousands, e.g. 5 019 000

# kind -> (regex, max acceptable-set size). Bare article refs are common, so they
# must be rarer to count as a localized fact.
KINDS = [("percent", RE_PERCENT, 3), ("article_para", RE_ARTPARA, 3),
         ("protocol", RE_PROTO, 3), ("date", RE_DATE, 4),
         ("figure", RE_FIGURE, 3), ("article", RE_ARTICLE, 2)]
TARGET = 250


def norm(s):
    return re.sub(r"\s+", " ", s.strip())


def content_words(text, token, k=3):
    tl = token.lower()
    seen, out = set(), []
    for w in re.findall(r"[A-Za-z]{5,}", text.lower()):
        if w in STOP or w in tl or w in seen:
            continue
        seen.add(w); out.append(w)
    out.sort(key=len, reverse=True)
    return out[:k]


def mine():
    sents = segment_corpus(load_corpus())
    tok2sents, tok2kind = defaultdict(set), {}
    for s in sents:
        for kind, pat, _cap in KINDS:
            for m in pat.findall(s.text):
                t = norm(m)
                tok2sents[t].add(s.id)
                tok2kind.setdefault(t, kind)   # first kind wins (specific before generic)
    cap = {k: c for k, _p, c in KINDS}
    # one probe per token, grouped by kind so we can balance the 250
    by_kind = defaultdict(list)
    for t, sids in sorted(tok2sents.items()):
        kind = tok2kind[t]
        if not (1 <= len(sids) <= cap[kind]):
            continue
        sid = min(sids)
        if tok2kind[t] == "date":
            q = f"what happened on {t}"
        else:
            cw = content_words(sents[sid].text, t)
            if not cw:
                continue
            q = f"{t} {' '.join(cw)}"
        by_kind[kind].append({"q": q, "token": t, "kind": kind, "support_sent": sid,
                              "acc": sorted(sids)})
    # round-robin across kinds up to TARGET, so no single kind dominates
    order = ["percent", "date", "protocol", "article_para", "figure", "article"]
    picked, i = [], 0
    pools = {k: by_kind.get(k, []) for k in order}
    while len(picked) < TARGET and any(pools.values()):
        k = order[i % len(order)]; i += 1
        if pools[k]:
            picked.append(pools[k].pop(0))
    json.dump({"questions": picked}, open(os.path.join(DATA, "exact_mined.json"), "w"),
              ensure_ascii=False, indent=1)
    cnt = defaultdict(int)
    for p in picked:
        cnt[p["kind"]] += 1
    print(f"wrote exact_mined.json: {len(picked)} probes {dict(cnt)}")
    for p in picked[:10]:
        print(f"  [{p['kind']:12}] {p['q']!r} acc={p['acc']}")


def _load(name):
    p = os.path.join(DATA, name)
    return json.load(open(p))["questions"] if os.path.exists(p) else []


def merge():
    sents = segment_corpus(load_corpus())
    eq = json.load(open(os.path.join(DATA, "eval_questions.json")))
    ans = json.load(open(os.path.join(DATA, "answer_sets.json")))
    # drop any previously merged exact additions (idempotent)
    keep = [q for q in eq["questions"] if q.get("category") not in ("exact", "exact_llm")]
    drop_ids = {q["id"] for q in eq["questions"] if q.get("category") in ("exact", "exact_llm")}
    for i in drop_ids:
        ans.pop(str(i), None)
    nxt = (max((q["id"] for q in keep), default=-1)) + 1
    added = 0
    for src_name, cat in (("exact_mined.json", "exact"), ("exact_llm.json", "exact_llm")):
        for r in _load(src_name):
            sid = r["support_sent"]
            s = sents[sid]
            keep.append({"id": nxt, "q": r["q"], "not_found": False, "support_sent": sid,
                         "page": s.page, "line": s.line, "expected_text": s.text[:200],
                         "misspelled": False, "category": cat,
                         "src": r.get("kind", cat)})
            ans[str(nxt)] = r["acc"]
            nxt += 1; added += 1
    eq["questions"] = keep
    eq["n_questions"] = len(keep)
    eq["n_in_document"] = sum(1 for q in keep if not q["not_found"])
    eq["n_out_of_domain"] = sum(1 for q in keep if q["not_found"])
    eq["n_misspelled_in_doc"] = sum(1 for q in keep if q["misspelled"] and not q["not_found"])
    eq["n_exact_in_doc"] = sum(1 for q in keep if q.get("category") in ("exact", "exact_llm"))
    json.dump(eq, open(os.path.join(DATA, "eval_questions.json"), "w"), ensure_ascii=False, indent=1)
    json.dump(ans, open(os.path.join(DATA, "answer_sets.json"), "w"), indent=1)
    indoc = eq["n_in_document"]
    print(f"merged {added} exact questions; in-doc now {indoc} "
          f"({eq['n_exact_in_doc']} exact = {eq['n_exact_in_doc']/indoc:.0%})")


if __name__ == "__main__":
    {"mine": mine, "merge": merge}[sys.argv[1] if len(sys.argv) > 1 else "mine"]()
