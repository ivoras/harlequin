# Experiment: do slot-key embeddings help memory search?

This documents an experiment evaluating whether the structured **memory slot
keys** can improve the hybrid memory search, and which integration performs best.
The reusable harness, dataset, and queries live under [`eval/memslot/`](../eval/memslot/).

## Background

Memory search (`internal/server/memory`) fuses two retrieval legs with
Reciprocal Rank Fusion (RRF):

1. **FTS5** lexical match on the memory *content*.
2. **Vector KNN** on the embedding of the memory *content*.

Separately, each memory may carry a structured **slot** — a normalized
`(key, value)` pair such as `organisation.name = "WoodChucks Inc."` — and the
**key** is embedded into its own `memory_slots_vec` table. Until this experiment
that key embedding was used only for *canonicalization* during slot extraction,
never for search.

The question: can the slot-key embeddings add a useful **attribute-match** signal
to search ranking, and if so, how should they be wired in?

## Change made first: humanize the key before embedding

Slot keys were embedded in their canonical dotted form (`organisation.name`),
which compares poorly to natural-language queries. We now embed a **humanized**
form (`"organisation name"`); the stored key column keeps the canonical form for
exact-match conflict detection. A `backfill-slot-keys` server subcommand
re-embeds existing slots. (`HumanizeKey`, `storeSlot`, `BackfillSlotKeyEmbeddings`.)

## Variants evaluated

A new tunable search, `Store.SearchTuned(..., slotWeight)`, adds an optional
third RRF leg over `memory_slots_vec`, weighted by `slotWeight` (0 = the
unchanged baseline `Search`). The variants:

- **baseline** — FTS + content-vector RRF, no slot leg (`slotWeight = 0`).
- **Option A** — add the slot-key leg at weight 1.0.
- **Option B** — add the slot-key leg at swept weights (weighted RRF).
- **Option C** — slot leg, but the slot vector embeds **humanized key + value**
  (e.g. `"product nimbus headphones price $225.99"`) instead of just the key
  (via `AddSlotEmbed`), at swept weights.

## Dataset & queries

Deterministically generated (`eval/memslot/gen`), kept as JSON for reuse:

- **1000 shared-scope memories** across four domains — products (640),
  employees (300), offices (32), vendors (28) — each a natural sentence plus an
  entity-specific hierarchical slot key, e.g.
  `product.nimbus_headphones.price = "$225.99"`.
- **154 queries** phrased as distracted, non-technical users type them, each
  with the target memory id(s):
  - "how much is the nimbus headphones"
  - "whats jane doe email"
  - "freight payment terms"
  - "what kind of product is the headphones"

These are **attribute-lookup** queries: the user asks *for* a value they don't
already know.

## Method

For each variant, load the dataset into a fresh temporary shared database
(embedding content + the slot text via the configured embeddings endpoint:
`ibm-granite/granite-embedding-311m-multilingual-r2`, dim 768), run all queries,
and compute **recall@k** (k = 1, 3, 5, 10) and **MRR** over the top 10 results.
Option C indexes a second store whose slot vectors carry key+value.

Run it:

```sh
go run ./eval/memslot/gen   # regenerate dataset (optional; committed)
CGO_ENABLED=1 CGO_CFLAGS="-I$(pwd)/third_party/sqlite/include" \
  go run -tags sqlite_fts5 ./eval/memslot/eval --config server.yaml
```

## Results

| variant                |   R@1 |   R@3 |   R@5 |  R@10 |   MRR |
|------------------------|------:|------:|------:|------:|------:|
| baseline (no slot)     | 0.799 | 0.877 | 0.929 | 0.948 | 0.849 |
| B: key w=0.25          | 0.812 | 0.877 | 0.942 | 0.948 | 0.857 |
| B: key w=0.50          | 0.812 | 0.877 | 0.935 | 0.955 | 0.858 |
| A: key w=1.0           | 0.812 | 0.890 | 0.935 | 0.955 | **0.860** |
| B: key w=2.00          | 0.799 | 0.896 | 0.935 | 0.955 | 0.854 |
| B: key w=4.00          | 0.786 | 0.896 | 0.935 | 0.955 | 0.847 |
| C: key+value w=0.25    | 0.792 | 0.883 | 0.922 | 0.948 | 0.846 |
| C: key+value w=0.50    | 0.779 | 0.877 | 0.922 | 0.948 | 0.837 |
| C: key+value w=1.00    | 0.727 | 0.857 | 0.916 | 0.942 | 0.802 |
| C: key+value w=2.00    | 0.649 | 0.844 | 0.916 | 0.942 | 0.759 |

## Findings

- **Option A (key-only slot leg, weight 1.0) is the best:** MRR 0.849 → 0.860,
  R@1 0.799 → 0.812, R@10 0.948 → 0.955. A small but consistent lift, mostly at
  the **top rank**.
- **Weight is a real knob (Option B):** low key weights (0.25–1.0) help R@1/MRR;
  **high weights regress** — at w=4.0 R@1 drops *below* baseline (0.786). The
  slot-key leg boosts every memory that shares an attribute key (many entities
  have a `…price`/`…email` slot), so too much weight pushes the exact target off
  the #1 spot while still keeping it in the top 3 (R@3 climbs to 0.896).
- **Option C (key+value) is worse than baseline at every weight and collapses as
  weight rises** (R@1 0.649, MRR 0.759 at w=2.0). The cause is structural: for
  attribute-lookup queries the user does not know the value, so the value text
  in the slot vector (`"$225.99"`, `"AB-1234"`, an email) is **noise** that pulls
  the slot match toward the wrong memory. Embedding the value would only help
  *value-mentioning* queries, which this realistic set doesn't contain.
- **Why the overall gains are modest:** the content/FTS legs already do well
  because the content names both entity and attribute, so content vectors usually
  retrieve the right memory; the key leg mainly sharpens the top rank.
- **Significance:** with 154 queries one query ≈ 0.0065, so the small A/B deltas
  are directional, not strongly significant. The Option C regression, by
  contrast, is large and unambiguous.

## Recommendation

- Ship the **key-only** slot leg (Option A / B) at a **modest weight (~0.5–1.0)**,
  behind a config flag; do not exceed ~1.0.
- **Do not** embed the value into the slot vector (Option C) for attribute-lookup
  workloads.
- `SearchTuned` already supports the key leg; production `Search` keeps
  `slotWeight = 0` (unchanged) until the flag is enabled.

## Caveats / future work

- Single embedding model (granite-311m); results may shift with a stronger model.
- Synthetic dataset with clean, entity-naming content — real memories are messier
  and the content legs may do worse, which could *raise* the slot leg's value.
- No value-mentioning queries were tested; a mixed workload might change the
  Option C verdict for those specific queries.
- The slot leg adds one KNN per scope per search (small cost; gate behind a flag).
