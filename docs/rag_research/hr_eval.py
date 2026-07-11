#!/usr/bin/env python3
"""Cross-lingual check: 20 EN treaty sentences (from the corpus) paired with
Croatian translations. Croatian side embeds as the query, English as the
document; a good multilingual embedder puts each HR sentence closest to its
own EN source (top-1 on the 20x20 cosine matrix) with a healthy margin."""
import os, sys, time
import numpy as np
import requests

PAIRS = [
    ("The Bank shall cooperate with all international organisations active in fields similar to its own.",
     "Banka surađuje sa svim međunarodnim organizacijama koje djeluju u područjima sličnima njezinima."),
    ("The responsibilities of the General Council are listed in full in Article 46 of this Statute.",
     "Odgovornosti Općeg vijeća u cijelosti su navedene u članku 46. ovog Statuta."),
    ("A reserve fund of up to 10 % of the subscribed capital shall be built up progressively.",
     "Postupno se izgrađuje pričuvni fond u visini do 10 % upisanog kapitala."),
    ("The Council shall act by a qualified majority after consulting the High Representative.",
     "Vijeće odlučuje kvalificiranom većinom nakon savjetovanja s Visokim predstavnikom."),
    ("The European Defence Agency shall be open to all Member States wishing to be part of it.",
     "Europska obrambena agencija otvorena je svim državama članicama koje u njoj žele sudjelovati."),
    ("The General Council shall be informed by the President of the ECB of decisions of the Governing Council.",
     "Predsjednik ESB-a obavješćuje Opće vijeće o odlukama Upravnog vijeća."),
    ("The Governing Council shall determine the denomination and remuneration of such claims.",
     "Upravno vijeće određuje denominaciju i naknadu za takva potraživanja."),
    ("The statistical data to be used for the application of this Protocol shall be provided by the Commission.",
     "Statističke podatke koji se koriste za primjenu ovog Protokola dostavlja Komisija."),
    ("Official correspondence and other official communications of the institutions of the Union shall not be subject to censorship.",
     "Službena prepiska i druga službena komunikacija institucija Unije ne podliježu cenzuri."),
    ("The Council, acting by a qualified majority, shall adopt a decision defining the Agency's statute, seat and operational rules.",
     "Vijeće kvalificiranom većinom donosi odluku kojom se utvrđuju statut, sjedište i pravila djelovanja Agencije."),
    ("This candidate shall be elected by the European Parliament by a majority of its component members.",
     "Tog kandidata bira Europski parlament većinom svojih članova."),
    ("Regulations concerning the calculation and determination of the required minimum reserves may be established by the Governing Council.",
     "Upravno vijeće može donijeti propise o izračunu i utvrđivanju obveznih minimalnih pričuva."),
    ("Within two months from such notification, those States may lodge at the Court statements of case or written observations.",
     "U roku od dva mjeseca od takve obavijesti te države mogu Sudu podnijeti podneske ili pisana očitovanja."),
    ("Decisions referred to in paragraph 1 shall commit the Member States in the positions they adopt and in the conduct of their activity.",
     "Odluke iz stavka 1. obvezuju države članice u stajalištima koja zauzimaju i u vođenju svojih aktivnosti."),
    ("Without prejudice to Article 18(2), the members of the Commission shall neither seek nor take instructions from any Government or other institution, body, office or entity.",
     "Ne dovodeći u pitanje članak 18. stavak 2., članovi Komisije ne traže i ne primaju upute ni od jedne vlade, institucije, tijela, ureda ili subjekta."),
    ("A director may be compulsorily retired by the Board of Governors only if he no longer fulfils the conditions required for the performance of his duties; the Board must act by a qualified majority.",
     "Vijeće guvernera može direktora prisilno umiroviti samo ako više ne ispunjava uvjete potrebne za obavljanje dužnosti; Vijeće mora odlučivati kvalificiranom većinom."),
    ("In such a case the other Member States shall abstain from formally placing the matter before the Council.",
     "U tom se slučaju ostale države članice suzdržavaju od formalnog iznošenja predmeta pred Vijeće."),
    ("They shall serve to strengthen cooperation between Member States and not to harmonise national systems.",
     "One služe jačanju suradnje među državama članicama, a ne usklađivanju nacionalnih sustava."),
    ("The Court of Justice of the European Union shall include the Court of Justice, the General Court and specialised courts.",
     "Sud Europske unije obuhvaća Sud, Opći sud i specijalizirane sudove."),
    ("The European Parliament shall forward its draft legislative acts and its amended drafts to national Parliaments.",
     "Europski parlament prosljeđuje svoje nacrte zakonodavnih akata i izmijenjene nacrte nacionalnim parlamentima."),
]

QWEN_Q = ("Instruct: Given a web search query, retrieve relevant passages that "
          "answer the query\nQuery: ")
MODELS = {
    "qwen3-emb-4b": dict(model="qwen/qwen3-embedding-4b", query_prefix=QWEN_Q),
    "mistral-embed": dict(model="mistralai/mistral-embed-2312", query_prefix=""),
}
URL = "https://openrouter.ai/api/v1/embeddings"
KEY = os.environ["OPENROUTER_API_KEY"]


def embed(model, texts):
    for attempt in range(5):
        try:
            r = requests.post(URL, headers={"Authorization": f"Bearer {KEY}"},
                              json={"model": model, "input": texts}, timeout=300)
            r.raise_for_status()
            d = sorted(r.json()["data"], key=lambda x: x["index"])
            v = np.asarray([x["embedding"] for x in d], dtype=np.float32)
            return v / np.linalg.norm(v, axis=1, keepdims=True)
        except Exception as e:
            if attempt == 4:
                raise
            print(f"retry ({e})", file=sys.stderr)
            time.sleep(2 * (attempt + 1))


en = [p[0] for p in PAIRS]
hr = [p[1] for p in PAIRS]
n = len(PAIRS)
print(f"{n} EN/HR pairs; HR = query side, EN = document side\n")
print(f"{'model':<15} {'top1':>6} {'diag¯':>7} {'offdiag¯':>9} {'margin¯':>8} {'min-margin':>11}")
for name, cfg in MODELS.items():
    dv = embed(cfg["model"], en)
    qv = embed(cfg["model"], [cfg["query_prefix"] + t for t in hr])
    sims = qv @ dv.T  # [hr, en]
    diag = np.diag(sims)
    off = sims[~np.eye(n, dtype=bool)]
    top1 = (sims.argmax(axis=1) == np.arange(n)).mean()
    # margin: own EN source vs best other EN sentence, per HR query
    rival = np.where(np.eye(n, dtype=bool), -np.inf, sims).max(axis=1)
    margin = diag - rival
    print(f"{name:<15} {top1:>6.2f} {diag.mean():>7.3f} {off.mean():>9.3f} "
          f"{margin.mean():>8.3f} {margin.min():>11.3f}")
    worst = margin.argmin()
    print(f"  tightest pair: #{worst} margin {margin[worst]:.3f} — {hr[worst][:70]!r}")
