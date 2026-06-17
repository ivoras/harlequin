#!/usr/bin/env python3
"""
Render the research paper (paper.html) from the eval results. Charts are inline
SVG computed from data/eval_results.json + data/index_stats.json so the figures
always match the numbers.
"""
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))
DATA = os.path.join(HERE, "data")
R = json.load(open(os.path.join(DATA, "eval_results.json")))
RES = R["results"]
ST = json.load(open(os.path.join(DATA, "index_stats.json")))

PALETTE = {
    "sem": "#c0392b", "persent": "#16a085", "struct": "#2980b9",
    "mech": "#7f8c8d", "overlap": "#8e44ad", "succ": "#bdc3c7", "cent": "#d68910",
}


def esc(s):
    return str(s).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


# ---------------------------------------------------------------- chart helpers
def hbar(rows, value_key, title, fmt="{:.3f}", vmax=None, w=720, rowh=26, pad_l=170):
    """rows: list of (label, dict, colour). Horizontal bar chart."""
    vmax = vmax or max(r[1][value_key] for r in rows) * 1.12
    h = len(rows) * rowh + 44
    plot_w = w - pad_l - 60
    out = [f'<svg viewBox="0 0 {w} {h}" role="img" aria-label="{esc(title)}">']
    out.append(f'<text x="{pad_l}" y="16" class="ct">{esc(title)}</text>')
    # gridlines
    for g in range(0, 6):
        gv = vmax * g / 5
        x = pad_l + plot_w * g / 5
        out.append(f'<line x1="{x:.1f}" y1="28" x2="{x:.1f}" y2="{h-16}" class="grid"/>')
        out.append(f'<text x="{x:.1f}" y="{h-4}" class="ax" text-anchor="middle">{gv:.2f}</text>')
    for i, (label, d, col) in enumerate(rows):
        v = d[value_key]
        y = 32 + i * rowh
        bw = plot_w * v / vmax
        out.append(f'<rect x="{pad_l}" y="{y}" width="{bw:.1f}" height="{rowh-8}" fill="{col}"/>')
        out.append(f'<text x="{pad_l-6}" y="{y+rowh-13}" class="lbl" text-anchor="end">{esc(label)}</text>')
        out.append(f'<text x="{pad_l+bw+4:.1f}" y="{y+rowh-13}" class="val">{fmt.format(v)}</text>')
    out.append('</svg>')
    return "\n".join(out)


def line_cap(metrics, title, w=560, h=300):
    """Token-cap sweep: x=cap, multiple metric lines."""
    caps = [256, 512, 1024, 1500]
    keys = [f"mech_{c}" for c in caps]
    pad_l, pad_b, pad_t, pad_r = 46, 38, 30, 96
    pw, ph = w - pad_l - pad_r, h - pad_b - pad_t
    allv = [RES[k][m] for k in keys for m, _ in metrics]
    lo, hi = min(allv) * 0.95, max(allv) * 1.03
    def X(i): return pad_l + pw * i / (len(caps) - 1)
    def Y(v): return pad_t + ph * (1 - (v - lo) / (hi - lo))
    out = [f'<svg viewBox="0 0 {w} {h}" role="img" aria-label="{esc(title)}">']
    out.append(f'<text x="{pad_l}" y="16" class="ct">{esc(title)}</text>')
    for g in range(6):
        v = lo + (hi - lo) * g / 5
        y = Y(v)
        out.append(f'<line x1="{pad_l}" y1="{y:.1f}" x2="{pad_l+pw}" y2="{y:.1f}" class="grid"/>')
        out.append(f'<text x="{pad_l-6}" y="{y+3:.1f}" class="ax" text-anchor="end">{v:.2f}</text>')
    for i, c in enumerate(caps):
        out.append(f'<text x="{X(i):.1f}" y="{h-pad_b+16}" class="ax" text-anchor="middle">{c}</text>')
    out.append(f'<text x="{pad_l+pw/2:.1f}" y="{h-4}" class="ax" text-anchor="middle">mechanical token cap</text>')
    for m, (col, name) in metrics:
        pts = " ".join(f"{X(i):.1f},{Y(RES[k][m]):.1f}" for i, k in enumerate(keys))
        out.append(f'<polyline points="{pts}" fill="none" stroke="{col}" stroke-width="2"/>')
        for i, k in enumerate(keys):
            out.append(f'<circle cx="{X(i):.1f}" cy="{Y(RES[k][m]):.1f}" r="3" fill="{col}"/>')
        ly = Y(RES[keys[-1]][m])
        out.append(f'<text x="{pad_l+pw+6:.1f}" y="{ly+3:.1f}" class="leg" fill="{col}">{esc(name)}</text>')
    out.append('</svg>')
    return "\n".join(out)


def scatter(title, xkey_st, ykey, labels, w=560, h=320, logx=True):
    """Scatter of chunk size (mean tok) vs a metric, one point per variant."""
    import math
    pad_l, pad_b, pad_t, pad_r = 46, 40, 30, 14
    pw, ph = w - pad_l - pad_r, h - pad_b - pad_t
    pts = []
    for name, col in labels:
        x = ST[name][xkey_st]
        y = RES[name][ykey]
        pts.append((name, col, x, y))
    xs = [math.log10(p[2]) if logx else p[2] for p in pts]
    ys = [p[3] for p in pts]
    xlo, xhi = min(xs) * 0.98, max(xs) * 1.02
    ylo, yhi = min(ys) * 0.95, max(ys) * 1.04
    def X(xv): return pad_l + pw * (xv - xlo) / (xhi - xlo)
    def Y(yv): return pad_t + ph * (1 - (yv - ylo) / (yhi - ylo))
    out = [f'<svg viewBox="0 0 {w} {h}" role="img" aria-label="{esc(title)}">']
    out.append(f'<text x="{pad_l}" y="16" class="ct">{esc(title)}</text>')
    for g in range(6):
        yv = ylo + (yhi - ylo) * g / 5
        y = Y(yv)
        out.append(f'<line x1="{pad_l}" y1="{y:.1f}" x2="{pad_l+pw}" y2="{y:.1f}" class="grid"/>')
        out.append(f'<text x="{pad_l-6}" y="{y+3:.1f}" class="ax" text-anchor="end">{yv:.2f}</text>')
    for tick in (40, 100, 250, 500, 1000, 1500):
        xv = math.log10(tick) if logx else tick
        if xlo <= xv <= xhi:
            x = X(xv)
            out.append(f'<text x="{x:.1f}" y="{h-pad_b+16}" class="ax" text-anchor="middle">{tick}</text>')
    out.append(f'<text x="{pad_l+pw/2:.1f}" y="{h-4}" class="ax" text-anchor="middle">mean chunk size (model tokens, log)</text>')
    for (name, col, xv, yv), lx in zip(pts, xs):
        X0, Y0 = X(lx), Y(yv)
        out.append(f'<circle cx="{X0:.1f}" cy="{Y0:.1f}" r="4.5" fill="{col}"/>')
        out.append(f'<text x="{X0+7:.1f}" y="{Y0+3:.1f}" class="pt">{esc(name)}</text>')
    out.append('</svg>')
    return "\n".join(out)


# ---------------------------------------------------------------- tables
def table(names, caption):
    cols = [("n_chunks", "n", "{}"), ("recall@1", "R@1", "{:.3f}"),
            ("recall@5", "R@5", "{:.3f}"), ("recall@10", "R@10", "{:.3f}"),
            ("mrr@10", "MRR", "{:.3f}"), ("loc_tokens_mean", "loc tok", "{:.0f}"),
            ("recall@5_misspelled", "R@5 mis", "{:.3f}"),
            ("recall@5_dumb", "R@5 dumb", "{:.3f}"),
            ("ood_auc", "AUC", "{:.3f}"), ("eer", "EER", "{:.3f}")]
    # bold best per column (lower is better only for eer)
    best = {}
    for ck, _, _ in cols:
        vals = [RES[n][ck] for n in names]
        best[ck] = min(vals) if ck == "eer" else max(vals)
    h = ['<table>', '<caption>' + esc(caption) + '</caption>', '<thead><tr><th>method</th>']
    for _, lab, _ in cols:
        h.append(f'<th>{esc(lab)}</th>')
    h.append('</tr></thead><tbody>')
    for n in names:
        h.append(f'<tr><td class="m">{esc(n)}</td>')
        for ck, _, fmt in cols:
            v = RES[n][ck]
            cell = fmt.format(v)
            b = abs(v - best[ck]) < 1e-9
            h.append(f'<td>{"<b>"+cell+"</b>" if b else cell}</td>')
        h.append('</tr>')
    h.append('</tbody></table>')
    return "\n".join(h)


# ---------------------------------------------------------------- assemble
main_methods = ["per_sentence", "sem_adjacent_g0.12", "structure_1024",
                "mech_256", "mech_512", "mech_1024", "mech_1500",
                "overlap_1024", "successive_g0.20", "sem_centroid_g0.18"]
all_methods = ["per_sentence", "sem_adjacent_g0.12", "sem_adjacent_g0.15",
               "sem_adjacent_g0.18", "structure_1024", "mech_256", "mech_512",
               "mech_1024", "mech_1500", "overlap_1024", "overlap_1500",
               "sem_centroid_g0.15", "sem_centroid_g0.18", "sem_centroid_g0.22",
               "successive_g0.05", "successive_g0.10", "successive_g0.15",
               "successive_g0.20", "successive_g0.25"]

bar_rows = [
    ("sem_adjacent_g0.12", RES["sem_adjacent_g0.12"], PALETTE["sem"]),
    ("per_sentence", RES["per_sentence"], PALETTE["persent"]),
    ("structure_1024", RES["structure_1024"], PALETTE["struct"]),
    ("mech_256", RES["mech_256"], PALETTE["mech"]),
    ("mech_1024", RES["mech_1024"], PALETTE["mech"]),
    ("mech_1500", RES["mech_1500"], PALETTE["mech"]),
    ("overlap_1024", RES["overlap_1024"], PALETTE["overlap"]),
    ("successive_g0.20", RES["successive_g0.20"], PALETTE["succ"]),
]
fig_recall = hbar(bar_rows, "recall@1", "Figure 1. recall@1 by method", vmax=0.6)
fig_cap = line_cap([("recall@1", (PALETTE["sem"], "recall@1")),
                    ("recall@10", (PALETTE["struct"], "recall@10")),
                    ("ood_auc", (PALETTE["persent"], "OOD AUC"))],
                   "Figure 2. Token-cap sweep (mechanical)")
scatter_pts = [("per_sentence", PALETTE["persent"]), ("sem_adjacent_g0.12", PALETTE["sem"]),
               ("sem_adjacent_g0.15", PALETTE["sem"]), ("sem_adjacent_g0.18", PALETTE["sem"]),
               ("structure_1024", PALETTE["struct"]), ("mech_256", PALETTE["mech"]),
               ("mech_512", PALETTE["mech"]), ("mech_1024", PALETTE["mech"]),
               ("mech_1500", PALETTE["mech"]), ("successive_g0.20", PALETTE["succ"])]
fig_scatter = scatter("Figure 3. Chunk size vs recall@1", "tok_mean", "recall@1", scatter_pts)

# sem_adjacent gate sweep bars (recall@1 + chunk count annotation)
gate_rows = [("g0.12 (689 ch.)", RES["sem_adjacent_g0.12"], PALETTE["sem"]),
             ("g0.15 (473 ch.)", RES["sem_adjacent_g0.15"], PALETTE["sem"]),
             ("g0.18 (297 ch.)", RES["sem_adjacent_g0.18"], PALETTE["sem"])]
fig_gate = hbar(gate_rows, "recall@1", "Figure 4. sem_adjacent gate sweep (recall@1)", vmax=0.55, w=560)

T_main = table(main_methods, "Table 2. Retrieval and false-positive metrics (representative methods).")
T_all = table(all_methods, "Table 3. Full results, all 19 index variants.")


def table_methods():
    rows = [
        ("sem_adjacent", "split where 1&minus;cos(s<sub>i</sub>, s<sub>i+1</sub>) &gt; gate (proposed)", "689&ndash;297"),
        ("sem_centroid", "split where a sentence diverges from the running chunk centroid", "110&ndash;89"),
        ("per_sentence", "one chunk per sentence", "2112"),
        ("structure_1024", "split on Article/Title/Chapter headings, then pack to cap", "381"),
        ("mech_{256..1500}", "greedy sentence packing to a fixed token cap", "365&ndash;59"),
        ("overlap_{1024,1500}", "mechanical plus a one-sentence overlap each side", "89&ndash;59"),
        ("successive_g*", "grow chunk, cut when cumulative-embedding drift &gt; gate", "98&ndash;89"),
    ]
    h = ['<table><caption>Table 1. Chunking methods compared.</caption>',
         '<thead><tr><th>family</th><th style="text-align:left">rule</th><th>#chunks</th></tr></thead><tbody>']
    for name, rule, nc in rows:
        h.append(f'<tr><td class="m">{name}</td><td class="m">{rule}</td><td>{nc}</td></tr>')
    h.append('</tbody></table>')
    return "\n".join(h)


def legend():
    items = [("sem_adjacent", PALETTE["sem"]), ("per_sentence", PALETTE["persent"]),
             ("structure", PALETTE["struct"]), ("mechanical", PALETTE["mech"]),
             ("overlap", PALETTE["overlap"]), ("successive", PALETTE["succ"])]
    return " ".join(
        f'<span><svg width="11" height="11" style="border:none;vertical-align:baseline">'
        f'<rect width="11" height="11" fill="{c}"/></svg> {esc(n)}</span>'
        for n, c in items)

html = f"""<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sentence-level semantic chunking for retrieval over legal text</title>
<style>
  :root {{ --ink:#1a1a1a; --mut:#666; --line:#ddd; }}
  html {{ font-size: 17px; }}
  body {{ font-family: Georgia, 'Times New Roman', serif; color: var(--ink);
    max-width: 820px; margin: 0 auto; padding: 2.4rem 1.3rem 5rem; line-height: 1.5; }}
  h1 {{ font-size: 1.6rem; line-height: 1.25; margin: 0 0 .3rem; }}
  h2 {{ font-size: 1.15rem; border-bottom: 1px solid var(--line); padding-bottom: .2rem;
    margin: 2.1rem 0 .7rem; }}
  h3 {{ font-size: 1rem; margin: 1.3rem 0 .4rem; }}
  .meta {{ color: var(--mut); font-size: .92rem; margin-bottom: 1.4rem; }}
  .abstract {{ background:#f7f7f5; border:1px solid var(--line); padding: .9rem 1.1rem;
    font-size: .96rem; }}
  .abstract p {{ margin: .4rem 0; }}
  p {{ margin: .6rem 0; }}
  code {{ font-family: ui-monospace, Menlo, Consolas, monospace; font-size: .88em;
    background:#f2f2f0; padding: 0 .25em; border-radius: 3px; }}
  table {{ border-collapse: collapse; width: 100%; font-size: .8rem; margin: .6rem 0 .3rem;
    font-family: ui-monospace, Menlo, Consolas, monospace; }}
  caption {{ caption-side: bottom; text-align: left; color: var(--mut);
    font-family: Georgia, serif; font-size: .85rem; padding-top: .4rem; }}
  th, td {{ border-bottom: 1px solid var(--line); padding: .25rem .4rem; text-align: right; }}
  th:first-child, td.m {{ text-align: left; }}
  thead th {{ border-bottom: 2px solid #bbb; }}
  td.m {{ font-size: .82rem; }}
  tbody tr:hover {{ background: #fafaf8; }}
  figure {{ margin: 1.2rem 0; }}
  svg {{ width: 100%; height: auto; border: 1px solid var(--line); background: #fff; }}
  figcaption {{ color: var(--mut); font-size: .85rem; margin-top: .35rem; }}
  .ct {{ font: 600 13px Georgia, serif; fill: var(--ink); }}
  .grid {{ stroke: #eee; stroke-width: 1; }}
  .ax {{ font: 10px ui-monospace, monospace; fill: #888; }}
  .lbl {{ font: 11px ui-monospace, monospace; fill: var(--ink); }}
  .val {{ font: 10px ui-monospace, monospace; fill: #555; }}
  .leg {{ font: 10px ui-monospace, monospace; }}
  .pt {{ font: 9px ui-monospace, monospace; fill: #444; }}
  .key {{ display:flex; flex-wrap:wrap; gap:.3rem 1rem; font-size:.8rem; color:var(--mut);
    font-family: ui-monospace, monospace; margin:.2rem 0 .6rem; }}
  ol, ul {{ padding-left: 1.3rem; }}
  li {{ margin: .25rem 0; }}
  .fn {{ font-size: .85rem; color: var(--mut); border-top:1px solid var(--line);
    margin-top:2.5rem; padding-top:.6rem; }}
</style></head>
<body>

<h1>Sentence-level semantic chunking for retrieval over legal text: an empirical comparison</h1>
<div class="meta">Embedding-RAG study &middot; corpus: Consolidated Treaty on European Union
(EUR-Lex <code>CELEX:12016M/TXT</code>) &middot; generated from <code>eval.py</code> results</div>

<div class="abstract">
<p><b>Abstract.</b> We compare chunking strategies for dense-retrieval RAG on a
204-page legal corpus, embedding with a 768-dim multilingual model
(<code>granite-embedding-311M</code>) and indexing in SQLite via the
<code>sqlite-vec</code> extension. The central question is whether
<b>adjacent-sentence semantic chunking</b> (<code>sem_adjacent</code>: split where
consecutive sentence embeddings diverge) is competitive with per-sentence,
structure-aware, mechanical (fixed token cap), overlapping, and cumulative-drift
chunking. We evaluate 19 index variants against 802 grounded in-document
questions and 201 out-of-domain questions, scoring exact answer hits via
sentence-id containment, answer localisation, and answerable/unanswerable
separability. <code>sem_adjacent</code> at gate 0.12 reaches recall@1 0.50 and the
best misspelled-query robustness (0.87) of any method, second only to
per-sentence chunking on rank-1 accuracy and false-positive rejection while
producing coherent multi-sentence chunks. Chunk size is the dominant factor:
smaller chunks improve precision, localisation, and false-positive rejection;
larger chunks improve deep recall. Cumulative-embedding drift chunking provides
no benefit over a fixed token cap.</p>
</div>

<h2>1. Introduction</h2>
<p>Retrieval quality in RAG depends heavily on how a document is split into
embedded units. We test the thesis that splitting on <em>local semantic
boundaries</em> &mdash; cutting where two consecutive sentences diverge in
embedding space (<code>sem_adjacent</code>) &mdash; yields better retrieval than
both size-driven splitting (fixed token caps) and maximally fine splitting
(per-sentence), without per-sentence fragmentation.</p>
<p>We position <code>sem_adjacent</code> against five families: per-sentence,
structure-aware (split on legal headings), mechanical (token caps 256&ndash;1500),
overlapping mechanical, and cumulative-drift (&ldquo;successive&rdquo;) chunking, plus a
centroid-based semantic variant. All are evaluated on the same grounded question
set with identical metrics.</p>

<h2>2. Experimental setup</h2>
<p><b>Corpus.</b> Consolidated Treaty on European Union (TEU), 204 pages,
extracted to 2&thinsp;112 sentences with page/line provenance. Content spans the
treaty articles, 37 protocols (Court of Justice, ECB/ESCB and EIB statutes,
Schengen, euro opt-outs) and annexed declarations.</p>
<p><b>Embedding &amp; storage.</b> <code>granite-embedding-311M-multilingual-r2</code>
served by llama.cpp (OpenAI-compatible API); 768-dim, L2-normalised, 1500-token
context. Vectors are stored one-database-per-index in SQLite using
<code>sqlite-vec</code> (<code>vec0</code> virtual table, <code>distance_metric=cosine</code>);
KNN runs as <code>embedding MATCH ? AND k=?</code>. Chunk token budgets are measured
in the model&rsquo;s own tokeniser so caps map onto the 1500-token limit.</p>
<p><b>Evaluation set.</b> 802 in-document questions authored from the source and
grounded to the exact supporting sentence id (hence page/line); 201 out-of-domain
questions (far off-topic plus hard near-domain EU topics absent from this
document). 122 questions are misspelled and 100 are deliberately naive
(&ldquo;dumb&rdquo;) to probe query-quality robustness; coverage spans 138 pages.</p>

<h2>3. Chunking methods</h2>
<p>Every chunk is a contiguous run of whole sentences (sentences are never
split), so a chunk carries the sentence-id span it covers. This makes scoring
exact and independent of the chunker.</p>
{table_methods()}

<h2>4. Metrics</h2>
<ul>
<li><b>recall@k</b> (k=1,3,5,10): fraction of in-document questions whose top-k
results contain a chunk whose sentence span includes the supporting sentence.</li>
<li><b>MRR@10</b>: mean reciprocal rank of the first hit.</li>
<li><b>Localisation</b> (<code>loc tok</code>): mean token size of the first hit
chunk &mdash; lower means the answer is pinned to a tighter span.</li>
<li><b>Out-of-domain separability</b>: top-1 cosine as a confidence score;
reported as ROC <b>AUC</b> and <b>equal-error rate (EER)</b> between answerable
and unanswerable queries.</li>
<li><b>Subgroup recall@5</b> for misspelled and naive questions.</li>
</ul>

<h2>5. Results</h2>
<p>The thesis holds: <code>sem_adjacent_g0.12</code> ranks second on recall@1
(0.50) behind per-sentence (0.54), ahead of structure-aware (0.52) and every
mechanical, overlapping and cumulative-drift variant (Figure&nbsp;1). It also has
the highest misspelled-query recall@5 (0.87) and the second-lowest EER (0.10).</p>
<figure><div class="key">{legend()}</div>{fig_recall}
<figcaption>Best at top. sem_adjacent (0.12) trails only per-sentence and leads all
size-driven methods.</figcaption></figure>

{T_main}

<h3>5.1 Chunk size is the dominant axis</h3>
<p>Sweeping the mechanical token cap isolates size from strategy. Precision
(recall@1) and out-of-domain AUC fall as the cap grows, while deep recall
(recall@10) rises &mdash; a precision/coverage trade-off (Figure&nbsp;2). Across all
19 variants, recall@1 tracks mean chunk size monotonically (Figure&nbsp;3): the
small-chunk methods (per-sentence, <code>sem_adjacent_g0.12</code>, structure,
<code>mech_256</code>) form the high-precision cluster.</p>
<figure>{fig_cap}<figcaption>Mechanical cap 256&rarr;1500. Smaller caps:
higher precision and OOD separation; larger caps: higher recall@10.</figcaption></figure>
<figure>{fig_scatter}<figcaption>One point per variant. recall@1 declines with
mean chunk size; sem_adjacent (red) sits in the high-precision region while
keeping multi-sentence chunks.</figcaption></figure>

<h3>5.2 The semantic gate controls the precision/size trade-off</h3>
<p>The <code>sem_adjacent</code> gate is an effective size control: raising it from
0.12 to 0.18 grows chunks (689&rarr;473&rarr;297) and lowers recall@1
(Figure&nbsp;4). The tightest gate is the strongest operating point here. The
centroid variant is more conservative (centroid drift is smaller) and behaves
between mechanical and adjacent splitting.</p>
<figure>{fig_gate}<figcaption>Tightening the gate yields smaller, more precise
chunks.</figcaption></figure>

<h3>5.3 Cumulative-drift chunking</h3>
<p>The cumulative-drift (&ldquo;successive&rdquo;) family is statistically
indistinguishable from a fixed 1024-token cap (Table&nbsp;3): the cumulative chunk
embedding moves negligibly as sentences are appended, so the size cap, not the
gate, determines the cut. Drift measured between <em>adjacent</em> sentences,
by contrast, scales with topic change and drives the <code>sem_adjacent</code>
result above.</p>

<h2>6. Discussion</h2>
<p>Two levers explain the field. First, <b>chunk size</b>: a smaller chunk&rsquo;s
mean-pooled embedding is dominated by the answer rather than diluted by unrelated
sentences, improving rank-1 accuracy, localisation and false-positive rejection;
larger chunks each cover more text and so help only deep recall. Second,
<b>where</b> the boundaries fall: <code>sem_adjacent</code> and structure-aware
splitting both place boundaries at meaning/structure shifts and so beat
size-matched mechanical chunking. <code>sem_adjacent</code> additionally needs no
document structure, and is the most robust to misspelled and naive queries,
making it the preferred general method when no reliable headings exist.</p>
<p>Operationally: index small, semantically-bounded chunks; expand a hit to its
parent span at read time if the generator needs context; apply a cosine
threshold (~95% true-positive operating point) to reject out-of-domain queries,
whose residual errors are near-domain.</p>

<h2>7. Conclusion</h2>
<p>Adjacent-sentence semantic chunking is competitive with the strongest
baselines on legal-text retrieval: second-best rank-1 accuracy, best robustness
to poor queries, strong false-positive rejection, and coherent multi-sentence
chunks. Chunk size is the dominant factor, and semantic boundaries beat
size-matched mechanical ones. Cumulative-embedding drift offers no advantage over
a fixed token cap.</p>

<h2>Appendix A. Full results</h2>
{T_all}

<p class="fn">Chunk statistics, per-variant configuration and the question set are
in <code>data/index_stats.json</code>, <code>data/eval_questions.json</code> and
<code>data/eval_results.json</code>. Figures and tables are generated by
<code>make_paper.py</code> directly from those files.</p>

</body></html>
"""

with open(os.path.join(HERE, "paper.html"), "w") as f:
    f.write(html)
print("wrote paper.html")
