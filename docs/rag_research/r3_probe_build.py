#!/usr/bin/env python3
"""
Build an EXACT-EXTRACTION probe set for the TEU corpus: queries anchored on an
exact token that dense embeddings tend to blur but lexical search nails
(percentages, protocol numbers, article+paragraph refs, specific figures).

Each probe is grounded: the query is the exact token plus a few salient content
words taken from a sentence that contains the token; the acceptable answer set is
every sentence that contains that exact token (duplicate-aware). We keep only
tokens that are rare in the corpus (well localized) so retrieval is unambiguous.

Writes data/probe_exact.json. No embedding server needed (pure text mining).
"""
import json
import os
import re
from collections import defaultdict

from lib import DATA, load_corpus, segment_corpus

STOP = set("the a an of to in and or for on by with as at is are be shall this that "
           "which it its from not no any all may than have has was were will would "
           "such other into under upon their they them then there where when who whom "
           "these those been being if but also each per via both more most".split())

# exact-token patterns -> (kind, compiled). Each match's group is normalised text.
PATTERNS = [
    ("percent", re.compile(r"\b\d{1,3}\s*%")),
    ("article_para", re.compile(r"\bArticle\s+\d+[a-z]?\(\d+\)")),
    ("protocol", re.compile(r"\bProtocol\s+\(?No\s*\d+\)?")),
]


def norm(s):
    return re.sub(r"\s+", " ", s.strip())


def content_words(text, token):
    toks = re.findall(r"[A-Za-z]{5,}", text.lower())
    tl = token.lower()
    seen, out = set(), []
    for w in toks:
        if w in STOP or w in tl or w in seen:
            continue
        seen.add(w)
        out.append(w)
    out.sort(key=len, reverse=True)      # longer ~ more specific
    return out[:3]


def main():
    sents = segment_corpus(load_corpus())
    # token -> {kind, sentences[ids]}
    tok2sents = defaultdict(set)
    tok2kind = {}
    for s in sents:
        for kind, pat in PATTERNS:
            for m in pat.findall(s.text):
                t = norm(m if isinstance(m, str) else m[0])
                if kind == "number":
                    n = int(re.sub(r"\D", "", t) or 0)
                    if 1900 <= n <= 2099 or n < 10:   # drop years and tiny ints
                        continue
                tok2sents[t].add(s.id)
                tok2kind[t] = kind

    probes, pid = [], 0
    for t, sids in sorted(tok2sents.items()):
        if not (1 <= len(sids) <= 3):            # well-localized tokens only
            continue
        sid = min(sids)
        cw = content_words(sents[sid].text, t)
        if not cw:
            continue
        probes.append({"id": pid, "q": f"{t} {' '.join(cw)}", "token": t,
                       "kind": tok2kind[t], "support_sent": sid,
                       "acc": sorted(sids), "not_found": False, "misspelled": False,
                       "category": "exact"})
        pid += 1

    json.dump({"questions": probes}, open(os.path.join(DATA, "probe_exact.json"), "w"),
              ensure_ascii=False, indent=1)
    by_kind = defaultdict(int)
    for p in probes:
        by_kind[p["kind"]] += 1
    print(f"wrote probe_exact.json: {len(probes)} probes  {dict(by_kind)}")
    for p in probes[:12]:
        print(f"  [{p['kind']:11}] q={p['q']!r}  acc={p['acc']}")


if __name__ == "__main__":
    main()
