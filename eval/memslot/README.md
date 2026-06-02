# Memory slot-key search evaluation

Measures whether adding a **slot-key embedding leg** to the hybrid memory search
(`Store.SearchTuned`) improves retrieval for sloppy, non-technical user queries.

## Layout

- `gen/` — deterministic dataset generator (pure stdlib).
- `data/memories.json` — 1000 shared-scope memories, each `{id, scope, content, key, value}`
  across four domains (products, employees, offices, vendors). Slot keys are
  hierarchical and entity-specific, e.g. `product.nimbus_headphones.price`.
- `data/queries.json` — 154 queries phrased as distracted, non-technical users
  type them ("how much is the nimbus headphones", "whats jane doe email",
  "freight payment terms"), each with the target memory id(s).
- `eval/` — harness: loads the dataset into a temp shared DB (embedding content +
  humanized slot keys via the configured embeddings endpoint) and reports
  recall@k and MRR for the baseline and the slot leg at several weights.

## Running

```sh
# regenerate the dataset (optional; committed copies exist)
go run ./eval/memslot/gen

# run the eval (needs the embeddings endpoint in server.yaml reachable)
CGO_ENABLED=1 CGO_CFLAGS="-I$(pwd)/third_party/sqlite/include" \
  go run -tags sqlite_fts5 ./eval/memslot/eval --config server.yaml
```

## Variants

- **baseline** — FTS + content-vector RRF (no slot leg); `SearchTuned(..., 0)`.
- **Option A** — baseline + slot-key RRF leg at weight 1.0.
- **Option B** — baseline + slot-key RRF leg at swept weights (weighted RRF).

## Results (granite-embedding-311m, 1000 memories, 154 queries)

| variant              |   R@1 |   R@3 |   R@5 |  R@10 |   MRR |
|----------------------|------:|------:|------:|------:|------:|
| baseline (no slot)   | 0.799 | 0.877 | 0.929 | 0.948 | 0.849 |
| B: slot w=0.25       | 0.812 | 0.877 | 0.942 | 0.948 | 0.857 |
| B: slot w=0.50       | 0.812 | 0.877 | 0.935 | 0.955 | 0.858 |
| A: slot w=1.0        | 0.812 | 0.883 | 0.935 | 0.955 | **0.859** |
| B: slot w=2.00       | 0.799 | 0.896 | 0.935 | 0.955 | 0.854 |
| B: slot w=4.00       | 0.786 | 0.896 | 0.935 | 0.955 | 0.847 |

## Interpretation

- The slot leg gives a **small but consistent lift** at modest weights. Best
  overall is **Option A (w=1.0)**: MRR 0.849 → 0.859, R@1 0.799 → 0.812
  (+2 queries), R@10 0.948 → 0.955.
- **Weight matters (Option B's point):** low weights (0.25–1.0) help R@1/MRR;
  **high weights regress** — at w=4.0 R@1 drops *below* baseline (0.786) because
  the slot leg over-boosts every memory sharing an attribute key (many entities
  have a `…price`/`…email` slot), pushing the exact target off the #1 spot while
  still keeping it in the top 3 (R@3 rises to 0.896).
- Gains are modest because the content/FTS legs already do well here — the
  generated content explicitly names entity + attribute, so content vectors
  usually nail it. The slot leg mainly sharpens the **top rank** (R@1, MRR).
- With only 154 queries, one query ≈ 0.0065, so these are directional rather
  than strongly significant — but the low-helps / high-hurts trend is clean.

**Recommendation:** if shipped, enable the slot leg at a **modest weight
(~0.5–1.0)** behind a config flag; do not exceed ~1.0. `SearchTuned` already
supports this; production `Search` keeps weight 0 (unchanged) until enabled.
