# Vector-embedding strategies for RAG: an empirical comparison

**Document under study:** Consolidated version of the Treaty on European Union
(EUR-Lex `CELEX:12016M/TXT`), 204 pages, 2 112 sentences after extraction. The
PDF contains the TEU itself (Articles 1–55), its 37 Protocols (Court of Justice
statute, ECB/ESCB statute, EIB statute, Schengen, the euro opt-outs, etc.) and
the Declarations annexed to the Final Act.

**Embedding model:** `granite-embedding-311M-multilingual-r2-Q8_0` served by
llama.cpp at `localhost:2235` (OpenAI-compatible API). 768-dim, L2-normalised
output, server context limit **1500 tokens** (physical batch size).

**Vector storage / search:** SQLite with the **sqlite-vec** (`vec0`) extension,
one database per index variant, KNN via `embedding MATCH ? AND k=?` with
`distance_metric=cosine`.

---

## 1. TL;DR

1. **Smaller chunks retrieve more precisely.** Per-sentence and structure-aware
   (Article-based) chunking dominate on rank-1 accuracy, MRR, answer
   localisation and false-positive rejection. The single best method for
   *pinpointing* the answer is **per-sentence** (recall@1 = 0.54, MRR = 0.65,
   median answer localised to a 57-token chunk).
2. **The token cap is the single most influential knob.** Sweeping the
   mechanical cap 256 → 512 → 1024 → 1500 (the model's max) monotonically
   *lowers* precision and false-positive separation but *raises* deep recall
   (recall@10). This is a precision-vs-coverage trade-off, not a free lunch.
3. **The brief's "successive" semantic-drift chunking degenerates into
   mechanical chunking** — but it can be *fixed* and made one of the best
   methods. As specified (re-embed the cumulative text, compare successive
   cumulative embeddings) the drift never exceeds 0.024, so the gate never fires
   and gates 0.10/0.15 and 0.20/0.25 produce identical indexes to `mech_1024`.
   Comparing **adjacent sentence** embeddings instead makes the gate bite and
   yields **`sem_adjacent_g0.12`: recall@1 = 0.50, the best misspelled-query
   robustness of any method, and the 2nd-best false-positive rejection** — at
   ~100× lower build cost. See §8.
4. **A single cosine threshold separates answerable from out-of-domain queries
   well** (AUC 0.93–0.97). Small chunks separate best (per-sentence AUC 0.972,
   equal-error-rate 9%).
5. **Naive ("dumb") and misspelled questions are measurably harder** — recall@5
   on the dumb subset is ~10–20 points below well-formed questions for every
   method.

The numbers below are produced end-to-end by the scripts in this directory and
stored in `data/eval_results.json` / `data/index_stats.json`.

---

## 2. Methods compared (chunking strategies)

All chunkers operate on **whole sentences** (never split a sentence) and a
chunk is always a *contiguous run of sentences*, which makes scoring exact (see
§5). Token budgets are measured in the **embedding model's own tokens** via
llama.cpp's `/tokenize` endpoint, so every cap maps directly onto the 1500-token
server limit.

| Strategy | Description |
|---|---|
| `mech_256/512/1024/1500` | **Mechanical**: greedily pack sentences up to the token cap. The cap sweep is the token-cap experiment (§4). |
| `overlap_1024/1500` | **Overlapping mechanical**: as mechanical, plus a 1-sentence overlap at each end; the overlap on a side is skipped if it would exceed the cap. |
| `successive_g0.05…0.25` | **Successive / semantic-drift**: grow a chunk one sentence at a time, re-embedding the cumulative text; cut when drift = `1 − cos(prev, curr)` exceeds the gate, or when the token cap (1024) would be exceeded. |
| `structure_1024` | **Structure-aware (novel)**: start a new chunk at every `Article`/`TITLE`/`CHAPTER`/`PROTOCOL`/`ANNEX` heading, then pack to the cap. Exploits the legal document's native structure. |
| `per_sentence` | **Per-sentence (novel)**: one chunk per sentence; maximally fine-grained retrieval. |
| `sem_adjacent_g*` | **Adjacent-sentence semantic (novel, §8)**: the fixed successive method — cut where consecutive sentences diverge (`1 − cos(sᵢ, sᵢ₊₁) > gate`). |
| `sem_centroid_g*` | **Centroid semantic (novel, §8)**: cut where a sentence diverges from the running chunk centroid. |

### A note on the "successive" gate

The brief specified: *"add sentences … until the similarity of vectors from two
successive runs falls below 0.1"*, swept over 0.05–0.25. Taken literally (cut
when `cos < gate`) the cut **never** fires — successive cumulative embeddings of
this corpus stay > 0.7 similar — so all gates would collapse to a pure
token-cap chunker. To make the sweep meaningful we operationalise the gate as a
**drift tolerance**: cut when `1 − cos(prev, curr) > gate`. Even so, the drift
rarely exceeds 0.05–0.25, so the token cap dominates and the gates barely
differ (see chunk counts below). **This is itself the finding:** incremental
"semantic" chunking adds essentially nothing over mechanical chunking for this
embedding model, because appending one sentence to an already-large chunk
perturbs its mean-pooled embedding only slightly. **§8 shows how to fix this**
by comparing *adjacent* sentences instead — which turns it into a top-tier
method.

### Chunk statistics (`data/index_stats.json`)

| variant | chunks | mean tok | median tok | max tok | sent/chunk |
|---|--:|--:|--:|--:|--:|
| mech_256 | 365 | 232 | 234 | 750 | 5.8 |
| mech_512 | 177 | 479 | 494 | 750 | 11.9 |
| mech_1024 | 89 | 952 | 999 | 1024 | 23.7 |
| mech_1500 | 59 | 1437 | 1465 | 1500 | 35.8 |
| overlap_1024 | 89 | 966 | 1006 | 1024 | 24.2 |
| overlap_1500 | 59 | 1446 | 1482 | 1500 | 36.3 |
| structure_1024 | 381 | 222 | 140 | 1024 | 5.5 |
| per_sentence | 2112 | 40 | 28 | 750 | 1.0 |
| successive_g0.05 | 98 | 865 | 989 | 1024 | 21.6 |
| successive_g0.10 | 91 | 932 | 996 | 1024 | 23.2 |
| successive_g0.15 | 91 | 932 | 996 | 1024 | 23.2 |
| successive_g0.20 | 89 | 952 | 999 | 1024 | 23.7 |
| successive_g0.25 | 89 | 952 | 999 | 1024 | 23.7 |

> Note the `max tok = 750` rows: a few sentences are giant enumerations (e.g.
> the EIB capital table listing every member state). The "never split a
> sentence" rule means such a sentence becomes an oversized single-sentence
> chunk; all remain < 1500 so none are truncated by the server.

---

## 3. The evaluation set (`data/eval_questions.json`)

- **802 in-document questions**, hand-authored from real content and **grounded
  to the exact supporting sentence id** (and thereby to a PDF page + line). The
  builder back-fills page/line/expected-text from the sentence, so the answer
  location can never drift from what was authored.
- **201 out-of-domain questions** for false-positive testing: a mix of
  *far* off-domain (cooking, sport, science) and deliberately *near*-domain EU
  topics that are **not** in this particular document (CAP subsidies, GDPR fines,
  Schengen visa fees, roaming caps, the 2024 elections, …). The near-domain set
  is the hard case — semantically EU-flavoured but unanswerable here.
- **122 misspelled** in-document questions (typos, dropped letters) and **100
  deliberately "dumb"/naive** questions ("is the euro real money?", "does Europe
  have an army?") tagged as a category, to probe robustness to poor query
  quality.
- Coverage: **138 distinct PDF pages**.

Categories and the misspelled/not-found flags are stored per question so the
evaluator can break results down by subgroup.

---

## 4. Token-cap investigation (added goal)

Isolating the cap with the mechanical sweep (only the budget changes):

| cap (model tok) | chunks | recall@1 | recall@5 | recall@10 | MRR@10 | loc. tokens | OOD AUC | EER |
|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| 256 | 365 | **0.439** | 0.731 | 0.813 | **0.563** | **236** | **0.959** | **0.104** |
| 512 | 177 | 0.403 | 0.706 | 0.807 | 0.534 | 483 | 0.948 | 0.120 |
| 1024 | 89 | 0.398 | 0.741 | 0.865 | 0.544 | 992 | 0.941 | 0.133 |
| 1500 | 59 | 0.389 | 0.723 | **0.872** | 0.535 | 1458 | 0.932 | 0.150 |

**Reading it:**
- **Precision (recall@1, MRR), localisation and false-positive rejection all
  improve as the cap shrinks.** A smaller chunk's mean-pooled embedding is
  dominated by the answer sentence instead of being diluted by ~35 unrelated
  sentences, so the right chunk ranks first more often and irrelevant chunks
  score lower (better AUC / EER).
- **Deep recall (recall@10) improves as the cap grows.** Bigger chunks each
  cover more material, so with 10 slots a large-chunk index is more likely to
  have *some* slot touching the answer — but that slot is a coarse, 1500-token
  block.
- The model's 1500-token capacity is therefore **usable but rarely optimal for
  retrieval precision**; the largest cap gives the best top-10 coverage and the
  worst pinpointing. Choose the cap from the downstream need: tight answer
  extraction → small cap; "stuff a lot into the LLM context" → large cap.

---

## 5. Evaluation criteria (metrics)

Defined in `eval.py`:

- **recall@k** (k = 1,3,5,10): fraction of in-doc questions whose top-k results
  include a chunk whose sentence span contains the supporting sentence. A
  **hit is exact** because chunks store `sent_start..sent_end` and questions
  store the supporting sentence id — independent of how the corpus was chunked.
- **MRR@10**: mean reciprocal rank of the first hit.
- **Content-localisation precision** (the headline "how precisely do embeddings
  hit the expected content" criterion): `loc_tokens_mean` = mean token size of
  the first hit chunk. Smaller ⇒ the answer is pinned to a tighter span.
- **page_recall@5**: looser check — any top-5 chunk covers the answer's PDF page.
- **False-positive handling**: top-1 cosine similarity is the retrieval
  confidence. We measure how well it separates answerable (in-doc) from
  unanswerable (OOD) queries: **AUC**, **equal-error-rate (EER)** and its
  threshold, and **FPR at the threshold that keeps 95 % of in-doc queries**.
- **Subgroup recall@5** for normal / misspelled / dumb questions.

---

## 6. Results (full, `data/eval_results.json`)

802 in-doc + 201 OOD questions. Best value per column in **bold**.

| variant | chunks | R@1 | R@3 | R@5 | R@10 | MRR@10 | page R@5 | loc tok | R@5 dumb | OOD AUC | EER | FPR@95 |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| per_sentence | 2112 | **0.541** | **0.726** | 0.789 | 0.837 | **0.647** | **0.897** | **57** | 0.524 | **0.972** | **0.090** | 0.149 |
| structure_1024 | 381 | 0.519 | 0.715 | **0.792** | 0.870 | 0.633 | 0.829 | 363 | **0.621** | 0.959 | 0.115 | 0.204 |
| mech_256 | 365 | 0.439 | 0.658 | 0.731 | 0.813 | 0.563 | 0.833 | 236 | 0.621 | 0.959 | 0.104 | **0.164** |
| mech_512 | 177 | 0.403 | 0.630 | 0.706 | 0.807 | 0.534 | 0.783 | 483 | 0.572 | 0.948 | 0.120 | 0.209 |
| mech_1024 | 89 | 0.398 | 0.642 | 0.741 | 0.865 | 0.544 | 0.794 | 992 | 0.586 | 0.941 | 0.133 | 0.269 |
| mech_1500 | 59 | 0.389 | 0.623 | 0.723 | 0.872 | 0.535 | 0.777 | 1458 | 0.586 | 0.932 | 0.150 | 0.304 |
| overlap_1024 | 89 | 0.400 | 0.652 | 0.754 | 0.884 | 0.553 | 0.798 | 1001 | 0.586 | 0.943 | 0.134 | 0.279 |
| overlap_1500 | 59 | 0.397 | 0.630 | 0.723 | **0.879** | 0.539 | 0.773 | 1467 | 0.586 | 0.938 | 0.143 | 0.269 |
| successive_g0.05 | 98 | 0.345 | 0.597 | 0.712 | 0.855 | 0.501 | 0.768 | 983 | 0.517 | 0.937 | 0.139 | 0.264 |
| successive_g0.10 | 91 | 0.397 | 0.641 | 0.738 | 0.865 | 0.542 | 0.792 | 992 | 0.572 | 0.939 | 0.135 | 0.279 |
| successive_g0.15 | 91 | 0.397 | 0.641 | 0.738 | 0.865 | 0.542 | 0.792 | 992 | 0.572 | 0.939 | 0.135 | 0.279 |
| successive_g0.20 | 89 | 0.398 | 0.642 | 0.741 | 0.865 | 0.544 | 0.794 | 992 | 0.586 | 0.941 | 0.133 | 0.269 |
| successive_g0.25 | 89 | 0.398 | 0.642 | 0.741 | 0.865 | 0.544 | 0.794 | 992 | 0.586 | 0.941 | 0.133 | 0.269 |

---

## 7. Analysis

**Precision & localisation.** `per_sentence` wins every precision metric and
localises answers to ~57 tokens — an order of magnitude tighter than the
1024/1500 chunkers (~1000–1460 tokens). For a RAG system that must quote or
extract a specific provision, this is decisive. Its weakness is `recall@10`
(0.837, the lowest of the large-context methods) and `R@5` (0.789): a single
sentence sometimes lacks the surrounding context that carries the query's
keywords (e.g. the Article number lives in a neighbouring heading sentence).

**Structure-aware chunking is the best all-rounder.** `structure_1024` matches
per-sentence on the dumb subset, has the **best recall@5 (0.792)**, strong
precision (R@1 0.519) and good FP separation, while producing only 381 chunks.
Because it cuts on `Article`/`TITLE` boundaries, each chunk is a coherent legal
unit, which both helps the embedding and aligns with how the answers are
organised. For legal/technical corpora with explicit structure this is the
recommended default.

**Overlap helps deep recall slightly.** `overlap_1024` beats `mech_1024` on
recall@5/@10 (0.754/0.884 vs 0.741/0.865) at negligible cost — the 1-sentence
bleed catches answers that straddle a chunk boundary. It does not improve
rank-1 precision.

**Successive/semantic chunking is not worth it here.** `g0.10`≡`g0.15` and
`g0.20`≡`g0.25`≡`mech_1024` to four decimals; only the most sensitive gate
(`g0.05`) differs, and it is the *worst* of the family (more mid-sized chunks,
fragmenting answers without the precision benefit of true per-sentence). It is
also by far the most expensive to build (one sequential embedding call per
sentence-step, ~8 000 calls vs a single batched pass). **This is a flaw of the
*cumulative* formulation, not of semantic chunking per se — §8 fixes it.**

**False positives.** Every method separates in-doc from OOD by top-1 cosine
with AUC ≥ 0.93. Small chunks are best (per-sentence AUC 0.972, EER 9.0 %;
mech_256 EER 10.4 %) because a tight, on-topic chunk yields a high score for
real questions and large chunks yield diffuse mid-range scores for everything.
The hard errors are the *near-domain* OOD questions (GDPR, CAP, roaming) — they
share vocabulary with the treaty and sit just under the decision threshold. A
practical RAG deployment should set a cosine cut-off around the 95%-TPR
threshold and treat below-threshold hits as "no answer found".

**Query quality matters.** Across methods the **dumb** subset scores ~0.52–0.62
recall@5 versus ~0.74–0.79 overall, and misspelling costs a few more points.
Fine-grained methods (per-sentence, structure) are the most robust to bad
queries, presumably because a vague query still aligns with *some* short, on-
topic chunk better than with a diluted megachunk.

---

## 8. Can the "successive" method be salvaged? Yes.

The brief's successive method failed for a precise, measurable reason. We
compared two drift signals over the whole corpus (`data/` exploration):

| drift signal | median | p90 | p95 | max |
|---|--:|--:|--:|--:|
| **cumulative** `1−cos(mean₀…ᵢ, mean₀…ᵢ₊₁)` (the brief) | 0.000 | 0.000 | 0.000 | **0.024** |
| **adjacent** `1−cos(sᵢ, sᵢ₊₁)` (proposed) | 0.126 | 0.196 | 0.207 | 0.238 |

The cumulative drift **maxes out at 0.024**, so any gate ≥ 0.05 produces *zero*
semantic cuts — the method can only ever cut on the token cap, which is why all
its gates collapse onto `mech_1024`. The cause is dilution: appending one
sentence to a 20-sentence chunk barely moves the mean-pooled embedding.

The adjacent-sentence drift, by contrast, lives exactly in the 0.05–0.25 band
the brief intended, so the gate becomes a real, monotonic control on chunk size.
Two fixed variants (both built from the **precomputed per-sentence embeddings**,
i.e. a single batched pass instead of ~8 000 sequential calls):

- **`sem_adjacent`** — cut between sentence *i* and *i+1* when
  `1 − cos(emb[i], emb[i+1]) > gate` (plus a token cap and a 2-sentence minimum).
- **`sem_centroid`** — cut when a new sentence diverges from the running
  *centroid* of the current chunk by more than the gate (robust to single
  outlier sentences).

Results:

| variant | chunks | sent/chunk | R@1 | R@5 | R@10 | MRR@10 | loc tok | R@5 misspelled | OOD AUC | EER |
|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|
| successive_g0.20 (brief) | 89 | 23.7 | 0.398 | 0.741 | 0.865 | 0.544 | 992 | 0.467 | 0.941 | 0.133 |
| **sem_adjacent_g0.12** | 689 | 3.1 | **0.501** | 0.776 | 0.852 | 0.615 | 182 | **0.867** | 0.964 | **0.100** |
| sem_adjacent_g0.15 | 473 | 4.5 | 0.456 | 0.721 | 0.801 | 0.565 | 316 | 0.667 | 0.954 | 0.115 |
| sem_adjacent_g0.18 | 297 | 7.1 | 0.388 | 0.678 | 0.781 | 0.513 | 592 | 0.467 | 0.949 | 0.123 |
| sem_centroid_g0.18 | 95 | 22.2 | 0.394 | 0.747 | 0.873 | 0.546 | 979 | 0.533 | 0.940 | 0.139 |
| *per_sentence (ref)* | 2112 | 1.0 | 0.541 | 0.789 | 0.837 | 0.647 | 57 | 0.800 | 0.972 | 0.090 |
| *structure_1024 (ref)* | 381 | 5.5 | 0.519 | 0.792 | 0.870 | 0.633 | 363 | 0.800 | 0.959 | 0.115 |

**What this shows:**
- The gate now *works*: tightening it (0.18 → 0.12) monotonically shrinks chunks
  and raises precision, exactly the behaviour the brief's sweep was meant to
  expose.
- **`sem_adjacent_g0.12` is a genuine top-tier method.** It lifts recall@1 from
  0.398 to 0.501 (+26 % relative over the brief's version), is the **single most
  robust method to misspelled queries** (0.867), and has the second-best
  false-positive rejection (EER 0.100), all while keeping coherent multi-
  sentence chunks (mean 3 sentences / 182 tokens) rather than per-sentence
  fragments. It also costs ~100× less to build than the cumulative method.
- `sem_centroid` is more conservative (centroid drift is smaller), so it sits
  between mechanical and adjacent; it is not worth the complexity here.

**Takeaway:** semantic chunking is useful for this model *only if the drift is
measured between sentences (or against a small running window), never as the
drift of the cumulative chunk embedding.* The brief's gate magnitudes were right
all along; the comparison was the bug.

## 9. Recommendations

- **Default for this kind of structured legal/technical document:**
  **structure-aware chunking** (split on Article/Title headings) capped well
  below the model limit (≈ 512 model tokens). Best balance of precision,
  recall@5 and robustness.
- **If answer pinpointing / quoting matters most:** index **per-sentence** (or
  small mechanical chunks) and, if you need context for the generator, expand
  the retrieved sentence to its parent Article at read time (store the parent
  span — cheap with the `sent_start/sent_end` we already keep).
- **Pick the token cap from the goal, not the model maximum.** The 1500-token
  capacity is best used for *coverage* (recall@10) when feeding a long LLM
  context; for precise retrieval, smaller is better.
- **If you want semantic chunking, use the adjacent-sentence variant**
  (`sem_adjacent`, gate ≈ 0.12), not the cumulative one. It is a top-3 method
  overall, the most robust to typos, and cheap to build. **Avoid the brief's
  cumulative-drift formulation** — it silently degenerates into mechanical
  chunking (§8).
- **Add a cosine threshold** (~95%-TPR operating point) to reject out-of-domain
  questions; expect near-domain queries to be the residual false positives.

---

## 10. Reproducing

```
docs/rag_research/
  extract_corpus.py     # PDF -> data/corpus.json (page/line addressable)
  lib.py                # embedder (cached), granite tokenizer, sqlite-vec store
  chunkers.py           # the chunking strategies
  build_indexes.py      # build every index variant -> data/indexes/*.sqlite
  build_questions.py    # assemble data/qsrc/*.jsonl -> data/eval_questions.json
  eval.py               # run retrieval + metrics -> data/eval_results.json
  vec0.so               # compiled sqlite-vec extension (asg017/sqlite-vec v0.1.6)
  data/qsrc/*.jsonl     # hand-authored question batches (in-doc + ood)
```

Run order (embedding server must be up at `localhost:2235`):

```bash
python3 extract_corpus.py      # once
python3 build_indexes.py       # builds/caches all vector DBs
python3 build_questions.py     # assembles the eval set
python3 eval.py                # prints the comparison table + writes results
```

Embeddings and token counts are cached in `data/embed_cache.sqlite`, so re-runs
are cheap and deterministic.

## 11. Limitations & threats to validity

- **One embedding model, one document, one language.** Findings about chunk size
  and semantic-drift are specific to mean-pooled `granite-embedding-311M` on a
  highly structured legal text; a model with different pooling or a narrative
  corpus could behave differently.
- **Questions are Claude-authored** from the source text. They are grounded to
  exact sentences (no answer drift) but share an author's phrasing distribution;
  the dumb/misspelled subsets partially mitigate this.
- **Per-subgroup counts are uneven** (e.g. misspelled questions per page), so
  the misspelled column is noisier than the headline metrics.
- **tiktoken vs granite tokens:** budgeting uses real granite tokens; the
  `n_tok` proxy is only used where noted. Brute-force vs `vec0` KNN give
  identical rankings here (exact search), so the sqlite-vec choice affects
  scalability, not the measured accuracy.
