#!/usr/bin/env python3
"""
Assemble the eval question set from hand-authored source batches.

Authoring format keeps each question compact so it can be grounded exactly:

  In-document batches  data/qsrc/in_*.jsonl   one JSON object per line:
      {"q": "<question text>", "s": <support sentence id>,
       "m": 0|1 (misspelled), "cat": "<category>"}

  Out-of-domain batch   data/qsrc/ood.jsonl:
      {"q": "<question text>", "m": 0|1, "cat": "<category>"}

This script:
  * validates every support sentence id exists,
  * back-fills page / line / expected_text from data/sentences.json so the
    answer location can never drift from what was authored,
  * assigns stable sequential question ids,
  * writes data/eval_questions.json (the canonical eval set) + prints stats.
"""
import glob
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
DATA = os.path.join(HERE, "data")
QSRC = os.path.join(DATA, "qsrc")
OUT = os.path.join(DATA, "eval_questions.json")


def load_jsonl(path):
    rows = []
    with open(path) as f:
        for ln, line in enumerate(f, 1):
            line = line.strip()
            if not line or line.startswith("//"):
                continue
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError as e:
                sys.exit(f"{path}:{ln}: bad JSON: {e}\n  {line}")
    return rows


def main():
    sents = {s["id"]: s for s in json.load(open(os.path.join(DATA, "sentences.json")))}
    questions = []
    qid = 0
    errors = []

    for path in sorted(glob.glob(os.path.join(QSRC, "in_*.jsonl"))):
        for r in load_jsonl(path):
            sid = r["s"]
            s = sents.get(sid)
            if s is None:
                errors.append(f"{os.path.basename(path)}: support sentence {sid} not found "
                              f"(q={r['q'][:50]!r})")
                continue
            exp = " ".join(s["text"].split())
            questions.append({
                "id": qid,
                "q": r["q"],
                "not_found": False,
                "support_sent": sid,
                "page": s["page"],
                "line": s["line"],
                "expected_text": exp[:200],
                "misspelled": bool(r.get("m", 0)),
                "category": r.get("cat", ""),
                "src": os.path.basename(path),
            })
            qid += 1

    ood_path = os.path.join(QSRC, "ood.jsonl")
    n_ood = 0
    if os.path.exists(ood_path):
        for r in load_jsonl(ood_path):
            questions.append({
                "id": qid,
                "q": r["q"],
                "not_found": True,
                "support_sent": None,
                "page": None,
                "line": None,
                "expected_text": None,
                "misspelled": bool(r.get("m", 0)),
                "category": r.get("cat", "ood"),
                "src": "ood.jsonl",
            })
            qid += 1
            n_ood += 1

    if errors:
        print("VALIDATION ERRORS:", file=sys.stderr)
        for e in errors:
            print("  " + e, file=sys.stderr)
        sys.exit(1)

    in_doc = [q for q in questions if not q["not_found"]]
    misspelled = [q for q in in_doc if q["misspelled"]]
    pages = {q["page"] for q in in_doc}
    payload = {
        "source_document": "Consolidated TEU+TFEU (CELEX:12016M/TXT)",
        "n_questions": len(questions),
        "n_in_document": len(in_doc),
        "n_out_of_domain": n_ood,
        "n_misspelled_in_doc": len(misspelled),
        "n_distinct_pages_covered": len(pages),
        "questions": questions,
    }
    with open(OUT, "w") as f:
        json.dump(payload, f, ensure_ascii=False, indent=1)
    print(f"wrote {OUT}")
    print(f"  total: {len(questions)}  in-doc: {len(in_doc)}  ood: {n_ood}")
    print(f"  misspelled (in-doc): {len(misspelled)}")
    print(f"  distinct pages covered: {len(pages)}")


if __name__ == "__main__":
    main()
