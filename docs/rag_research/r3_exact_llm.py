#!/usr/bin/env python3
"""
Claude-extracted exact-ish named-entity questions (the "LLM" step).

The candidate phrases below were extracted by Claude from the TEU/TFEU as
distinctive exact-match search targets the regex miner does not cover: treaty /
instrument names, the formal names of Member States, institutions & bodies, key
terms of art, seats, and notable dates. Each candidate is GROUNDED here against
the real corpus (kept only if it occurs verbatim), so nothing is hallucinated.

Grounding is duplicate-aware so even very common entities yield a localized
question: support = the most content-rich sentence containing the phrase; the
acceptable set is that sentence plus other occurrences that lexically overlap it
(Jaccard >= 0.5). The query pairs the exact phrase with salient words from the
support sentence. -> data/exact_llm.json
"""
import json
import os
import re
from collections import defaultdict

from lib import DATA, load_corpus, segment_corpus

TARGET = 100
JACCARD = 0.5
ACC_CAP = 8

STOP = set("the a an of to in and or for on by with as at is are be shall this that "
           "which it its from not no any all may than have has was were will would "
           "such other into under upon their they them then there where when who whom "
           "these those been being if but also each per via both more most".split())

TREATIES = [
    "Treaty on European Union", "Treaty on the Functioning of the European Union",
    "Treaty establishing the European Community",
    "Treaty establishing the European Atomic Energy Community",
    "Single European Act", "Charter of Fundamental Rights of the European Union",
    "Treaty of Lisbon", "Treaty of Amsterdam", "Treaty of Nice", "Treaty of Rome",
    "Final Act", "Statute of the Court of Justice of the European Union",
    "Statute of the European Investment Bank", "Schengen acquis",
    "Statute of the European System of Central Banks and of the European Central Bank",
]
INSTITUTIONS = [
    "European Parliament", "European Council", "European Commission",
    "Council of the European Union", "Court of Justice of the European Union",
    "Court of Justice", "European Central Bank", "Court of Auditors",
    "European Investment Bank", "Economic and Social Committee",
    "Committee of the Regions", "European System of Central Banks",
    "European Ombudsman", "General Court", "Governing Council",
    "Political and Security Committee", "Eurojust", "Europol",
    "European Economic Community", "European Defence Agency",
    "High Representative of the Union for Foreign Affairs and Security Policy",
    "European External Action Service", "Economic and Financial Committee",
    "Permanent Representatives", "Executive Board",
    "President of the European Council", "President of the Commission",
    "Secretary-General", "Eurogroup", "European Investment Fund",
]
COUNTRIES = [
    "Kingdom of Belgium", "Republic of Bulgaria", "Czech Republic",
    "Kingdom of Denmark", "Federal Republic of Germany", "Republic of Estonia",
    "Hellenic Republic", "Kingdom of Spain", "French Republic", "Italian Republic",
    "Republic of Cyprus", "Republic of Latvia", "Republic of Lithuania",
    "Grand Duchy of Luxembourg", "Republic of Hungary", "Republic of Malta",
    "Kingdom of the Netherlands", "Republic of Austria", "Republic of Poland",
    "Portuguese Republic", "Republic of Slovenia", "Slovak Republic",
    "Republic of Finland", "Kingdom of Sweden",
    "United Kingdom of Great Britain and Northern Ireland", "United Kingdom",
    "Swiss Confederation", "Principality of Liechtenstein", "Kingdom of Norway",
    "Republic of Iceland",
]
TERMS = [
    "Economic and Monetary Union", "area of freedom, security and justice",
    "common foreign and security policy", "common security and defence policy",
    "qualified majority", "internal market", "enhanced cooperation",
    "ordinary legislative procedure", "European citizenship", "customs union",
    "free movement of goods", "free movement of capital", "price stability",
    "monetary policy", "subsidiarity", "proportionality", "own resources",
    "excessive deficit", "Stability and Growth Pact", "European Atomic Energy Community",
    "Court of First Instance", "Eurosystem", "free movement of persons",
    "free movement of services", "fundamental rights", "rule of law",
    "human dignity", "sustainable development", "common commercial policy",
    "development cooperation", "humanitarian aid", "common agricultural policy",
    "European Social Fund", "structured cooperation",
]
SEATS = ["Brussels", "Strasbourg", "Frankfurt", "the Hague", "Luxembourg", "euro", "ecu"]
DATES = ["7 February 1992", "13 December 2007", "25 March 1957", "1 January 1958",
         "1 January 1999", "1 May 2004", "1 December 2009", "1 November 1993",
         "1 January 2002"]

CANDIDATES = ([("treaty", p) for p in TREATIES] + [("institution", p) for p in INSTITUTIONS]
              + [("country", p) for p in COUNTRIES] + [("term", p) for p in TERMS]
              + [("seat", p) for p in SEATS] + [("date", p) for p in DATES])
ORDER = ["treaty", "institution", "country", "term", "seat", "date"]


def cwords(text):
    return set(w for w in re.findall(r"[a-z]{4,}", text.lower()) if w not in STOP)


def salient(text, phrase, k=3):
    pl = phrase.lower()
    seen, out = set(), []
    for w in re.findall(r"[A-Za-z]{5,}", text.lower()):
        if w in STOP or w in pl or w in seen:
            continue
        seen.add(w); out.append(w)
    out.sort(key=len, reverse=True)
    return out[:k]


def main():
    sents = segment_corpus(load_corpus())
    low = [s.text.lower() for s in sents]
    by_kind = defaultdict(list)
    for kind, phrase in CANDIDATES:
        pl = phrase.lower()
        occ = [s.id for s in sents if pl in low[s.id]]
        if not occ:
            continue
        support = max(occ, key=lambda i: len(cwords(sents[i].text)))
        sw = cwords(sents[support].text)
        acc = [i for i in occ
               if len(cwords(sents[i].text) & sw) / max(1, len(cwords(sents[i].text) | sw)) >= JACCARD]
        if support not in acc:
            acc.append(support)
        acc = sorted(acc)[:ACC_CAP]
        if kind == "date":
            q = f"what happened on {phrase}"
        else:
            cw = salient(sents[support].text, phrase)
            if not cw:
                continue
            q = f"{phrase} {' '.join(cw)}"
        by_kind[kind].append({"q": q, "token": phrase, "kind": kind,
                              "support_sent": support, "acc": acc})
    # round-robin across kinds up to TARGET
    picked, i = [], 0
    pools = {k: by_kind.get(k, []) for k in ORDER}
    while len(picked) < TARGET and any(pools.values()):
        k = ORDER[i % len(ORDER)]; i += 1
        if pools[k]:
            picked.append(pools[k].pop(0))
    json.dump({"questions": picked}, open(os.path.join(DATA, "exact_llm.json"), "w"),
              ensure_ascii=False, indent=1)
    cnt = defaultdict(int)
    for r in picked:
        cnt[r["kind"]] += 1
    print(f"wrote exact_llm.json: {len(picked)} grounded {dict(cnt)}")
    for r in picked[:12]:
        print(f"  [{r['kind']:11}] {r['q']!r} acc(n={len(r['acc'])})={r['acc'][:3]}")


if __name__ == "__main__":
    main()
