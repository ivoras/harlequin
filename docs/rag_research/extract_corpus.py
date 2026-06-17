#!/usr/bin/env python3
"""
Extract the TEU/TFEU PDF into a structured, line-addressable corpus.

Output: data/corpus.json
  {
    "source": "...",
    "n_pages": 204,
    "pages": [
       {"page": 1, "lines": ["line0", "line1", ...]},
       ...
    ]
  }

We use poppler's `pdftotext -layout` page by page so that line numbers are
stable and we can ground every eval question to a (page, line) location.

Line numbers are 0-based within a page. Blank lines are preserved so that
line indices stay aligned with what a human sees in `pdftotext -layout`.
"""
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
PDF = os.path.join(HERE, "data", "teu_tfeu.pdf")
OUT = os.path.join(HERE, "data", "corpus.json")


def n_pages(pdf: str) -> int:
    out = subprocess.check_output(["pdfinfo", pdf], text=True)
    for line in out.splitlines():
        if line.startswith("Pages:"):
            return int(line.split(":", 1)[1].strip())
    raise RuntimeError("could not determine page count")


def page_text(pdf: str, page: int) -> str:
    return subprocess.check_output(
        ["pdftotext", "-layout", "-f", str(page), "-l", str(page), pdf, "-"],
        text=True,
    )


def main():
    if not os.path.exists(PDF):
        sys.exit(f"missing {PDF}")
    total = n_pages(PDF)
    pages = []
    for p in range(1, total + 1):
        txt = page_text(PDF, p)
        # rstrip a trailing formfeed/newline but keep internal blank lines
        lines = txt.replace("\f", "").rstrip("\n").split("\n")
        pages.append({"page": p, "lines": lines})
        if p % 25 == 0:
            print(f"  extracted page {p}/{total}", file=sys.stderr)
    doc = {
        "source": "https://eur-lex.europa.eu/legal-content/EN/TXT/PDF/?uri=CELEX:12016M/TXT",
        "title": "Consolidated versions of the TEU and TFEU (2016/C 202/01)",
        "n_pages": total,
        "pages": pages,
    }
    with open(OUT, "w") as f:
        json.dump(doc, f, ensure_ascii=False)
    n_lines = sum(len(p["lines"]) for p in pages)
    print(f"wrote {OUT}: {total} pages, {n_lines} lines")


if __name__ == "__main__":
    main()
