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
- **Option C** — baseline + slot leg where the slot vector embeds the
  **humanized key + value** (`"product nimbus headphones price $225.99"`) rather
  than just the key, indexed into a second store, swept weights.

## Results (granite-embedding-311m, 1000 memories, 154 queries)

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

## Interpretation

- **Option A (key, w=1.0) is the winner:** MRR 0.849 → 0.860, R@1 0.799 → 0.812,
  R@10 0.948 → 0.955. A small but consistent lift.
- **Weight matters (Option B):** low key weights (0.25–1.0) help R@1/MRR; **high
  weights regress** — at w=4.0 R@1 drops *below* baseline (0.786) because the
  slot leg over-boosts every memory sharing an attribute key (many entities have
  a `…price`/`…email` slot), pushing the exact target off #1 while keeping it in
  the top 3 (R@3 rises to 0.896).
- **Option C (key+value) is worse than baseline at every weight, and degrades
  sharply as weight rises** (R@1 0.649, MRR 0.759 at w=2.0). The reason is
  structural: these are *attribute-lookup* queries — the user asks **for** the
  value and does not know it, so the value text in the slot vector
  (`"$225.99"`, `"AB-1234"`, an email) is **pure noise** against the query and
  pulls the slot match away from the right memory. Embedding the value would
  only help *value-oriented* queries (where the user mentions the value), which
  this realistic query set doesn't contain.
- Gains overall are modest because content/FTS already do well — the content
  names entity + attribute, so content vectors usually nail it; the key leg
  mainly sharpens the **top rank**.
- With 154 queries, one query ≈ 0.0065, so the small A/B differences are
  directional; the C regression, however, is large and unambiguous.

**Recommendation:** ship the **key-only** slot leg (Option A / B) at a **modest
weight (~0.5–1.0)** behind a config flag; do not exceed ~1.0, and **do not embed
the value into the slot vector (Option C)** for attribute-lookup workloads.
`SearchTuned` supports the key leg; production `Search` keeps weight 0
(unchanged) until enabled.
