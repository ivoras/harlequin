#!/usr/bin/env python3
"""
Convert the plain-text second corpus (Project Gutenberg) into the same
page/line-addressable schema as corpus.json, for the generalization test.
Strips the Gutenberg header/footer and synthesizes ~45-line "pages".
"""
import json
import os
import re

HERE = os.path.dirname(os.path.abspath(__file__))
DATA = os.path.join(HERE, "data")
RAW = os.path.join(DATA, "corpus2_raw.txt")
OUT = os.path.join(DATA, "corpus2.json")
PAGE_LINES = 45


def main():
    txt = open(RAW, encoding="utf-8").read()
    # strip Gutenberg boilerplate
    m1 = re.search(r"\*\*\* START OF (THE|THIS) PROJECT GUTENBERG.*?\*\*\*", txt, re.S)
    m2 = re.search(r"\*\*\* END OF (THE|THIS) PROJECT GUTENBERG", txt)
    if m1:
        txt = txt[m1.end():]
    if m2:
        txt = txt[:m2.start()]
    lines = [ln.rstrip() for ln in txt.split("\n")]
    # drop the very long leading front-matter (contents, prefaces) up to INTRODUCTION
    pages, cur, pno = [], [], 1
    for ln in lines:
        cur.append(ln)
        if len(cur) >= PAGE_LINES:
            pages.append({"page": pno, "lines": cur}); cur = []; pno += 1
    if cur:
        pages.append({"page": pno, "lines": cur})
    doc = {"source": "https://www.gutenberg.org/ebooks/1228",
           "title": "On the Origin of Species (Darwin) — generalization corpus",
           "n_pages": len(pages), "pages": pages}
    json.dump(doc, open(OUT, "w"), ensure_ascii=False)
    print(f"wrote {OUT}: {len(pages)} pages, {sum(len(p['lines']) for p in pages)} lines")


if __name__ == "__main__":
    main()
