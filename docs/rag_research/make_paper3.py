#!/usr/bin/env python3
"""
Render paper3.html: the single-embedder selection study. Terse, data-driven.
Every table is sortable (click a header) and every column header carries a
plain-language tooltip. Figures are inline SVG computed from the same numbers.

Reads data/r3_agg.json (from r3_aggregate.py) plus lib.EMBEDDERS for the prompt
conventions, and an optional generalization file data/r3_corpus2.json.
"""
import json
import math
import os

from lib import DATA, EMBEDDERS, MODELS, MODES_OF

HERE = os.path.dirname(os.path.abspath(__file__))
AGG = json.load(open(os.path.join(DATA, "r3_agg.json")))
C2 = (json.load(open(os.path.join(DATA, "r3_corpus2.json")))
      if os.path.exists(os.path.join(DATA, "r3_corpus2.json")) else None)

# stable colour per physical model
MC = {"granite": "#2980b9", "snowflake": "#16a085", "qwen06b": "#c0392b",
      "gemma": "#8e44ad", "lfm2": "#e67e22"}
PRETTY = {"granite": "granite-311M", "snowflake": "snowflake-arctic-l-v2.0",
          "qwen06b": "Qwen3-Emb-0.6B", "gemma": "embeddinggemma-300M",
          "lfm2": "LFM2.5-Emb-350M"}


def esc(s):
    return str(s).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


TIPS = {
    "model": "Embedding model (served alone on :2235, doing boundaries + chunk + query vectors)",
    "mode": "Prompt mode: native = model's documented query/doc prefixes; no-prefix = raw text both sides (Harlequin today)",
    "query prefix": "Exact string prepended to a query before embedding (native convention)",
    "doc prefix": "Exact string prepended to a passage before embedding (native convention)",
    "chunker": "Chunking rule and target size",
    "lexical": "Lexical/fusion arm: dense only, BM25, FTS5, or a dense+lexical RRF hybrid",
    "gate": "sem_adjacent cut threshold: cut between sentences when 1-cos(adjacent) exceeds it",
    "tok": "Mean chunk size in this model's own tokens",
    "n": "Number of chunks the document was split into",
    "R@1": "Answer recall@1: the #1 retrieved chunk covers an acceptable answer sentence",
    "R@5": "Answer recall@5: an acceptable answer chunk is in the top 5",
    "R@10": "Answer recall@10: an acceptable answer chunk is in the top 10",
    "ans@256": "Hit within the top chunks summing to ≤256 tokens — equal retrieved-context budget (size-neutral)",
    "ans@1024": "Hit within the top chunks summing to ≤1024 tokens — equal retrieved-context budget (size-neutral)",
    "MRR": "Mean reciprocal rank of the first hit within the top 10",
    "mis": "recall@5 on the misspelled-query subgroup",
    "dumb": "recall@5 on the naive ('dumb') query subgroup",
    "AUC": "AUROC of the top-1 score separating in-document from out-of-domain queries (higher = better rejection)",
    "EER": "Equal error rate of in-doc vs out-of-domain separation (lower = better)",
    "dim": "Embedding dimension (vector storage + cosine search cost per chunk)",
    "vec/s": "Raw embedding throughput on this host: corpus sentences embedded per second",
    "score": "Pinpoint+rejection composite: weighted mean of z-scored R@1, MRR, AUC (2×) and R@5, ans@1024 (1×)",
    "rank": "Overall rank by the composite score",
    "label": "Recommended triple: embedder/chunker/lexical_backend",
    "carried": "✓ marks the prompt mode carried into all later chapters (the better of native vs no-prefix here)",
}


def th(label):
    t = TIPS.get(label, "")
    return f'<th title="{esc(t)}">{esc(label)}</th>' if t else f"<th>{esc(label)}</th>"


def table(cols, rows, caption, bold=None):
    """cols: list of (key,label,fmt). bold: dict key->('max'|'min') to embolden best."""
    best = {}
    if bold:
        for k, how in bold.items():
            vals = [r[k] for r in rows if r.get(k) is not None]
            if vals:
                best[k] = (min if how == "min" else max)(vals)
    h = ['<table class="sortable"><caption>%s</caption><thead><tr>' % caption
         + "".join(th(l) for _, l, _ in cols) + "</tr></thead><tbody>"]
    for r in rows:
        h.append("<tr>")
        for i, (k, _l, fmt) in enumerate(cols):
            v = r.get(k)
            cell = "-" if v is None else (esc(v) if fmt is None else fmt.format(v))
            if k in best and v is not None and abs(v - best[k]) < 1e-9:
                cell = f"<b>{cell}</b>"
            cls = ' class="m"' if i == 0 and not isinstance(v, (int, float)) else ""
            h.append(f"<td{cls}>{cell}</td>")
        h.append("</tr>")
    h.append("</tbody></table>")
    return "\n".join(h)


# ---------------- charts ----------------
def curve(series, title, xlabel, ylabel, marks=None, w=660, h=340, logx=True):
    pad_l, pad_b, pad_t, pad_r = 52, 42, 28, 150
    pw, ph = w - pad_l - pad_r, h - pad_b - pad_t
    xs = [p[0] for _, _, pts in series for p in pts]
    ys = [p[1] for _, _, pts in series for p in pts]
    fx = (lambda v: math.log10(v)) if logx else (lambda v: v)
    xlo, xhi = min(map(fx, xs)), max(map(fx, xs))
    ylo, yhi = min(ys) * 0.96, max(ys) * 1.04
    X = lambda v: pad_l + pw * (fx(v) - xlo) / (xhi - xlo + 1e-9)
    Y = lambda v: pad_t + ph * (1 - (v - ylo) / (yhi - ylo + 1e-9))
    o = [f'<svg viewBox="0 0 {w} {h}" role="img">',
         f'<text x="{pad_l}" y="15" class="ct">{esc(title)}</text>']
    for g in range(6):
        yv = ylo + (yhi - ylo) * g / 5
        o.append(f'<line x1="{pad_l}" y1="{Y(yv):.0f}" x2="{pad_l+pw}" y2="{Y(yv):.0f}" class="grid"/>')
        o.append(f'<text x="{pad_l-6}" y="{Y(yv)+3:.0f}" class="ax" text-anchor="end">{yv:.2f}</text>')
    xt = ([40, 80, 160, 320, 640, 1280] if logx else
          sorted({round(v, 2) for v in xs}))
    for tick in xt:
        if xlo <= fx(tick) <= xhi:
            o.append(f'<text x="{X(tick):.0f}" y="{h-pad_b+15}" class="ax" text-anchor="middle">{tick}</text>')
    o.append(f'<text x="{pad_l+pw/2:.0f}" y="{h-3}" class="ax" text-anchor="middle">{esc(xlabel)}</text>')
    o.append(f'<text x="14" y="{pad_t+ph/2:.0f}" class="ax" text-anchor="middle" transform="rotate(-90 14 {pad_t+ph/2:.0f})">{esc(ylabel)}</text>')
    for name, col, pts in series:
        pts = sorted(pts)
        poly = " ".join(f"{X(x):.1f},{Y(y):.1f}" for x, y in pts)
        o.append(f'<polyline points="{poly}" fill="none" stroke="{col}" stroke-width="2"/>')
        for x, y in pts:
            o.append(f'<circle cx="{X(x):.1f}" cy="{Y(y):.1f}" r="2.6" fill="{col}"/>')
    if marks:
        for x, y, col in marks:
            o.append(f'<circle cx="{X(x):.1f}" cy="{Y(y):.1f}" r="5.5" fill="none" stroke="{col}" stroke-width="2"/>')
    lx = pad_l + pw + 12
    for i, (name, col, _p) in enumerate(series):
        ly = pad_t + 4 + i * 16
        o.append(f'<rect x="{lx}" y="{ly}" width="10" height="10" fill="{col}"/>')
        o.append(f'<text x="{lx+14}" y="{ly+9}" class="leg">{esc(name)}</text>')
    o.append("</svg>")
    return "\n".join(o)


def grouped(cats, groups, title, vmax, w=720, h=330):
    pad_l, pad_b, pad_t, pad_r = 44, 76, 28, 150
    pw, ph = w - pad_l - pad_r, h - pad_b - pad_t
    gw = pw / len(cats); bw = gw / (len(groups) + 0.5)
    o = [f'<svg viewBox="0 0 {w} {h}" role="img">',
         f'<text x="{pad_l}" y="15" class="ct">{esc(title)}</text>']
    for g in range(6):
        yv = vmax * g / 5; y = pad_t + ph * (1 - g / 5)
        o.append(f'<line x1="{pad_l}" y1="{y:.0f}" x2="{pad_l+pw}" y2="{y:.0f}" class="grid"/>')
        o.append(f'<text x="{pad_l-5}" y="{y+3:.0f}" class="ax" text-anchor="end">{yv:.2f}</text>')
    for ci, cat in enumerate(cats):
        x0 = pad_l + ci * gw
        o.append(f'<text x="{x0+gw/2:.0f}" y="{h-pad_b+14}" class="ax" text-anchor="end" '
                 f'transform="rotate(-35 {x0+gw/2:.0f} {h-pad_b+14})">{esc(cat)}</text>')
        for gi, (name, col, vals) in enumerate(groups):
            v = vals.get(cat)
            if v is None:
                continue
            bh = ph * v / vmax; x = x0 + 0.25 * bw + gi * bw
            o.append(f'<rect x="{x:.1f}" y="{pad_t+ph-bh:.1f}" width="{bw*0.9:.1f}" height="{bh:.1f}" fill="{col}"/>')
    for gi, (name, col, _v) in enumerate(groups):
        o.append(f'<rect x="{pad_l+pw+10}" y="{pad_t+gi*16}" width="10" height="10" fill="{col}"/>')
        o.append(f'<text x="{pad_l+pw+24}" y="{pad_t+gi*16+9}" class="leg">{esc(name)}</text>')
    o.append("</svg>")
    return "\n".join(o)


# ---------------- chapters ----------------
def ch_naming():
    return """<h2>2. Naming convention</h2>
<p>Every result set is named <code>embedder/chunker/lexical_backend</code>:</p>
<ul>
<li><b>embedder</b> &mdash; one of the five models, in its carried prompt mode
(<code>_np</code> suffix marks the no-prefix mode).</li>
<li><b>chunker</b> &mdash; the boundary rule and target size
(<code>per_sentence</code>, <code>mech_256…mech_1500</code>,
<code>overlap_512/1024</code>, <code>sem_adjacent</code>).</li>
<li><b>lexical_backend</b> &mdash; the retrieval/fusion arm: <code>dense</code>
(sqlite-vec cosine), <code>bm25</code>, <code>fts5</code>, or their dense+lexical
RRF hybrids <code>bm25_hybrid</code> and <code>fts5_hybrid</code>.</li>
</ul>
<p>Example: <code>lfm2/sem_adjacent/bm25_hybrid</code> is the LFM2.5 embedder (native
prompts), adjacent-sentence semantic chunks, dense+BM25 RRF.</p>"""


def ch_prompts():
    cols = [("model", "model", None), ("query", "query prefix", None),
            ("doc", "doc prefix", None)]
    rows = []
    for m in MODELS:
        cfg = EMBEDDERS[m]
        q = cfg["query_prefix"] or "(none)"
        d = cfg["doc_prefix"] or "(none)"
        rows.append({"model": PRETTY[m],
                     "query": q.replace("\n", "↵"), "doc": d})
    body = ['<table class="sortable"><caption>Table 1. Native prompt conventions '
            '(documented per model). granite is symmetric (no prompt); the others '
            'are asymmetric. The no-prefix mode drops both.</caption><thead><tr>'
            + "".join(th(l) for _, l, _ in cols) + "</tr></thead><tbody>"]
    for r in rows:
        body.append(f'<tr><td class="m">{esc(r["model"])}</td>'
                    f'<td class="m"><code>{esc(r["query"])}</code></td>'
                    f'<td class="m"><code>{esc(r["doc"])}</code></td></tr>')
    body.append("</tbody></table>")
    return ("<h2>3. Native prompt conventions</h2>\n"
            "<p>Embedding models are trained with model-specific instruction "
            "prefixes. Symmetric models (granite) prepend nothing; asymmetric "
            "retrieval models prepend a query instruction and, for some, a "
            "document tag. Harlequin currently sends <i>raw text</i> (no prefix) "
            "on both sides, so for every model we measure both the native "
            "convention and the no-prefix mode (§4).</p>\n" + "\n".join(body))


def ch_prefix_gap():
    rows = []
    carried = AGG["carried"]
    for r in AGG["prefix_gap"]:
        m = r["model"]
        rows.append({
            "model": PRETTY[m], "mode": "native" if r["native"] else "no-prefix",
            "recall@1": r["recall@1"], "recall@5": r["recall@5"],
            "answer@1024tok": r["answer@1024tok"], "ood_auc": r["ood_auc"],
            "composite": r.get("composite"),
            "carried": "✓" if carried.get(m) == r["mode"] else ""})
    cols = [("model", "model", None), ("mode", "mode", None),
            ("recall@1", "R@1", "{:.3f}"), ("recall@5", "R@5", "{:.3f}"),
            ("answer@1024tok", "ans@1024", "{:.3f}"), ("ood_auc", "AUC", "{:.3f}"),
            ("composite", "score", "{:+.2f}"), ("carried", "carried", None)]
    return ("<h2>4. Prompt mode: native prefixes vs no-prefix</h2>\n"
            "<p>On a fixed reference chunker (<code>" + esc(AGG["ref_chunker"])
            + "</code>, dense), we compare each model's native prompts against "
            "raw text. The better mode (✓) is carried into every later "
            "chapter. granite has a single (symmetric) mode.</p>\n"
            + table(cols, rows, "Table 2. Prefix gap on the reference chunker. "
                    "'carried' marks the mode used downstream."))


def ch_gate():
    out = ["<h2>5. Semantic-adjacency gate sweep</h2>",
           "<p>The <code>sem_adjacent</code> chunker cuts between two sentences "
           "when their embedding cosine distance exceeds a gate. The gate's scale "
           "is model-specific (drift distributions differ), so it is swept per "
           "model at percentiles of that model's adjacent-drift distribution. "
           "Finer gates yield smaller chunks and — as everywhere — higher "
           "pinpoint scores; we pick each model's gate by the same "
           "pinpoint+rejection composite used to rank models, and carry that "
           "<code>sem_adjacent</code> variant forward.</p>"]
    s_r1, s_ans, marks_r1, marks_ans = [], [], [], []
    for m in MODELS:
        g = AGG["gate"].get(m)
        if not g:
            continue
        pts1 = [(r["tok_mean"], r["recall@1"]) for r in g["rows"]]
        ptsa = [(r["tok_mean"], r["answer@1024tok"]) for r in g["rows"]]
        s_r1.append((PRETTY[m], MC[m], pts1))
        s_ans.append((PRETTY[m], MC[m], ptsa))
        bg = next(r for r in g["rows"] if r["variant"] == g["best_variant"])
        marks_r1.append((bg["tok_mean"], bg["recall@1"], MC[m]))
        marks_ans.append((bg["tok_mean"], bg["answer@1024tok"], MC[m]))
    if s_r1:
        out.append("<figure>" + curve(s_r1, "Figure 1. sem_adjacent: recall@1 vs "
                   "chunk size (gate swept per model)", "mean chunk size (tokens, log)",
                   "recall@1", marks=marks_r1) + "<figcaption>One line per model; each "
                   "point is a gate. Circled = the gate selected by the composite.</figcaption></figure>")
        out.append("<figure>" + curve(s_ans, "Figure 2. sem_adjacent: answer@1024 "
                   "(equal-budget) vs chunk size", "mean chunk size (tokens, log)",
                   "answer@1024", marks=marks_ans) + "<figcaption>Size-neutral view: hit "
                   "within a 1024-token retrieved budget.</figcaption></figure>")
    rows = []
    for m in MODELS:
        g = AGG["gate"].get(m)
        if not g:
            continue
        bg = next(r for r in g["rows"] if r["variant"] == g["best_variant"])
        rows.append({"model": PRETTY[m], "gate": g["best_gate"], "tok": bg["tok_mean"],
                     "recall@1": bg["recall@1"], "recall@5": bg["recall@5"],
                     "answer@1024tok": bg["answer@1024tok"], "ood_auc": bg["ood_auc"]})
    cols = [("model", "model", None), ("gate", "gate", "{:.3f}"), ("tok", "tok", "{:.0f}"),
            ("recall@1", "R@1", "{:.3f}"), ("recall@5", "R@5", "{:.3f}"),
            ("answer@1024tok", "ans@1024", "{:.3f}"), ("ood_auc", "AUC", "{:.3f}")]
    out.append(table(cols, rows, "Table 3. Selected sem_adjacent gate per model."))
    return "\n".join(out)


CHUNK_COLS = [("chunker", "chunker", None), ("tok_mean", "tok", "{:.0f}"),
              ("recall@1", "R@1", "{:.3f}"), ("recall@5", "R@5", "{:.3f}"),
              ("recall@10", "R@10", "{:.3f}"), ("answer@256tok", "ans@256", "{:.3f}"),
              ("answer@1024tok", "ans@1024", "{:.3f}"), ("mrr@10", "MRR", "{:.3f}"),
              ("ood_auc", "AUC", "{:.3f}"), ("eer", "EER", "{:.3f}")]


def ch_chunkers():
    out = ["<h2>6. Chunker comparison</h2>",
           "<p>Eight chunkers per model, dense retrieval, carried prompt mode: "
           "per-sentence, four mechanical token caps, two overlapping caps, and "
           "the model's selected <code>sem_adjacent</code>. Smaller chunks win on "
           "pinpoint metrics and OOD rejection; larger caps trade that for deep "
           "recall. <code>answer@B</code> compares at an equal retrieved-token "
           "budget.</p>",
           "<p><b>How a chunk is indexed.</b> Every chunker emits the same kind of "
           "object: a contiguous run of whole sentences that becomes one "
           "<i>atomic record</i>, keyed by a shared chunk id across all three "
           "structures &mdash; the <code>chunks</code> table (text + sentence-span "
           "provenance), the sqlite-vec <code>vec0</code> index (one cosine "
           "vector), and an FTS5 row (a single <code>text</code> field, "
           "<code>rowid</code> = chunk id). The chunker decides only <i>what a "
           "record is</i> &mdash; where the boundaries fall &mdash; while that same "
           "record is what dense, BM25 and FTS5 each retrieve; because the id is "
           "shared, their ranked lists fuse directly by id under RRF (§7). A "
           "chunker therefore affects retrieval only by changing record boundaries "
           "and sizes, which is exactly what the table below isolates.</p>"]
    cats = ["per_sentence", "mech_256", "mech_512", "mech_1024", "mech_1500",
            "overlap_512", "overlap_1024", "sem_adjacent"]
    groups = []
    for m in MODELS:
        crows = AGG["chunkers"].get(m)
        if not crows:
            continue
        vals = {}
        for r in crows:
            key = "sem_adjacent" if r["chunker"].startswith("sem_adjacent") else r["chunker"]
            vals[key] = r["recall@1"]
        groups.append((PRETTY[m], MC[m], vals))
    if groups:
        out.append("<figure>" + grouped(cats, groups,
                   "Figure 3. recall@1 by chunker × model (dense)", 0.7)
                   + "<figcaption>Carried prompt mode, dense retrieval.</figcaption></figure>")
    for m in MODELS:
        crows = AGG["chunkers"].get(m)
        if not crows:
            continue
        out.append(f"<h3>6.{MODELS.index(m)+1} {esc(PRETTY[m])} "
                   f"(<code>{esc(AGG['carried'][m])}</code>)</h3>")
        out.append(table(CHUNK_COLS, crows,
                   f"Table 4.{MODELS.index(m)+1}. Chunkers under {esc(PRETTY[m])}, dense.",
                   bold={"recall@1": "max", "recall@5": "max", "answer@1024tok": "max",
                         "mrr@10": "max", "ood_auc": "max", "eer": "min"}))
    return "\n".join(out)


def ch_lexical():
    out = ["<h2>7. Lexical backend: BM25 vs FTS5 (+RRF)</h2>",
           "<p>For each model's best dense chunker we compare lexical arms: a "
           "custom numpy Okapi BM25 and SQLite's built-in FTS5 (its own BM25 + "
           "tokenizer), standalone and as a dense+lexical RRF hybrid. Exact tokens "
           "(article numbers, &lsquo;qualified majority&rsquo;) matter for legal "
           "text, so the hybrid is the relevant comparison.</p>"]
    cols = [("lexical", "lexical", None), ("recall@1", "R@1", "{:.3f}"),
            ("recall@5", "R@5", "{:.3f}"), ("recall@10", "R@10", "{:.3f}"),
            ("answer@1024tok", "ans@1024", "{:.3f}"), ("mrr@10", "MRR", "{:.3f}"),
            ("ood_auc", "AUC", "{:.3f}")]
    for m in MODELS:
        lx = AGG["lexical"].get(m)
        if not lx:
            continue
        out.append(f"<h3>7.{MODELS.index(m)+1} {esc(PRETTY[m])} "
                   f"(base <code>{esc(lx['base_chunker'])}</code>)</h3>")
        out.append(table(cols, lx["rows"],
                   f"Table 5.{MODELS.index(m)+1}. Lexical arms on "
                   f"{esc(PRETTY[m])}/{esc(lx['base_chunker'])}.",
                   bold={"recall@1": "max", "recall@5": "max", "answer@1024tok": "max",
                         "mrr@10": "max", "ood_auc": "max"}))
    return "\n".join(out)


def ch_selection():
    rows = []
    for r in AGG["selection"]:
        rows.append({
            "rank": r["rank"], "label": r["label"], "dim": r["dim"],
            "vec/s": r.get("vec_per_sec"),
            "recall@1": r["recall@1"], "recall@5": r["recall@5"],
            "answer@1024tok": r["answer@1024tok"], "mrr@10": r["mrr@10"],
            "ood_auc": r["ood_auc"], "eer": r["eer"], "composite": r["composite"]})
    cols = [("rank", "rank", "{}"), ("label", "label", None), ("dim", "dim", "{}"),
            ("vec/s", "vec/s", "{:.0f}"), ("recall@1", "R@1", "{:.3f}"),
            ("recall@5", "R@5", "{:.3f}"), ("answer@1024tok", "ans@1024", "{:.3f}"),
            ("mrr@10", "MRR", "{:.3f}"), ("ood_auc", "AUC", "{:.3f}"),
            ("eer", "EER", "{:.3f}"), ("composite", "score", "{:+.2f}")]
    win = AGG["selection"][0] if AGG["selection"] else None
    alt = AGG["selection"][1] if len(AGG["selection"]) > 1 else None
    rec = ""
    if win:
        alt_txt = ""
        if alt:
            cheaper = (alt["dim"] < win["dim"]) or (alt.get("vec_per_sec", 0)
                                                    > 1.3 * (win.get("vec_per_sec") or 1))
            alt_txt = (f" The cost-efficient runner-up is <code>{esc(alt['label'])}</code> "
                       f"(dim {alt['dim']}, {alt.get('vec_per_sec', 0):.0f} vec/s vs "
                       f"{win.get('vec_per_sec', 0):.0f}, recall@1 {alt['recall@1']:.3f}); "
                       + ("prefer it if index size or ingest throughput dominates."
                          if cheaper else "the winner also leads on cost."))
        rec = (f"<p><b>Recommendation.</b> The best model/chunker/lexical triple is "
               f"<code>{esc(win['label'])}</code> (dim {win['dim']}, "
               f"recall@1 {win['recall@1']:.3f}, recall@5 {win['recall@5']:.3f}, "
               f"AUC {win['ood_auc']:.3f}). Cost is reported alongside quality: "
               f"dimension drives vec0 storage and cosine-search cost, throughput "
               f"the ingestion rate. Quality decides the ranking; cost is the "
               f"tie-breaker to override the pick.{alt_txt}</p>")
    return ("<h2>8. Model selection (composite + cost)</h2>\n"
            "<p>Each model is represented by its own best (chunker, lexical_backend) "
            "by a pinpoint+rejection composite (z-scored R@1, MRR, AUC at 2×; "
            "R@5, ans@1024 at 1×). Models are ranked by the same composite; "
            "dimension and throughput are reported but not scored.</p>\n"
            + table(cols, rows, "Table 6. Best pipeline per model, ranked by "
                    "composite. Cost columns (dim, vec/s) are informational.")
            + "\n" + rec)


def ch_generalization():
    if not C2:
        return ""
    rows = C2["results"]
    fams = {"semadj": ("semantic", "#c0392b"), "fixed": ("fixed", "#27ae60"),
            "random": ("random", "#e67e22")}
    series = []
    for fam, (lab, col) in fams.items():
        pts = [(r["tok_mean"], r["recall1"]) for r in rows if r["family"] == fam]
        if pts:
            series.append((lab, col, pts))
    fig = curve(series, "Figure 4. Generalization: recall@1 vs chunk size",
                "mean chunk size (tokens, log)", "recall@1")
    return ("<h2>9. Generalization</h2>\n"
            f"<p>Replication of the winner (<code>{esc(C2['model'])}</code>, one "
            f"model the whole path) on {C2['n_questions']} grounded questions over "
            "a contrasting non-legal corpus (Darwin, <i>Origin of Species</i>). The "
            "same size law and semantic≥random ordering hold.</p>\n"
            f"<figure>{fig}<figcaption>The size law generalizes off legal text; "
            "semantic boundaries sit at or above random at matched size.</figcaption></figure>")


def main():
    win = AGG["selection"][0] if AGG["selection"] else None
    abstract = (
        "We select a single embedding model for Harlequin document ingestion by "
        "evaluating five small open embedders &mdash; granite-311M, "
        "snowflake-arctic-l-v2.0, Qwen3-Embedding-0.6B, embeddinggemma-300M and "
        "LFM2.5-Embedding-350M &mdash; each run as the <i>only</i> model on the whole "
        "path (sentence boundaries, chunk vectors and query vectors), on a 204-page "
        "legal corpus with 802 grounded and 201 out-of-domain questions, scored by "
        "duplicate-aware, size-normalised cosine retrieval metrics with bootstrap CIs. "
        "We report native-prompt vs no-prefix behaviour, sweep the semantic-chunking "
        "gate per model, compare eight chunkers and two lexical backends (custom BM25 "
        "vs SQLite FTS5) under RRF, and rank models by a pinpoint+rejection composite "
        "with a cost table.")
    if win:
        abstract += (f" The recommended triple is <code>{esc(win['label'])}</code> "
                     f"(dim {win['dim']}, recall@1 {win['recall@1']:.3f}).")

    body = "\n".join([
        ch_naming(), ch_prompts(), ch_prefix_gap(), ch_gate(), ch_chunkers(),
        ch_lexical(), ch_selection(), ch_generalization(),
    ])

    html = f"""<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Selecting an embedding model for Harlequin: a single-embedder RAG study</title>
<style>
 html{{font-size:17px}} body{{font-family:Georgia,serif;color:#1a1a1a;max-width:860px;
  margin:0 auto;padding:2.4rem 1.3rem 5rem;line-height:1.5}}
 h1{{font-size:1.55rem;line-height:1.25;margin:0 0 .3rem}}
 h2{{font-size:1.15rem;border-bottom:1px solid #ddd;padding-bottom:.2rem;margin:2rem 0 .7rem}}
 h3{{font-size:1rem;margin:1.2rem 0 .4rem}}
 .meta{{color:#666;font-size:.92rem;margin-bottom:1.3rem}}
 .abstract{{background:#f7f7f5;border:1px solid #ddd;padding:.9rem 1.1rem;font-size:.96rem}}
 code{{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:.86em;background:#f2f2f0;padding:0 .25em;border-radius:3px}}
 table{{border-collapse:collapse;width:100%;font-size:.78rem;margin:.6rem 0;font-family:ui-monospace,monospace}}
 caption{{caption-side:bottom;text-align:left;color:#666;font-size:.84rem;padding-top:.4rem;font-family:Georgia,serif}}
 th,td{{border-bottom:1px solid #ddd;padding:.24rem .4rem;text-align:right}}
 th{{cursor:pointer;user-select:none;white-space:nowrap;border-bottom:2px solid #bbb}}
 th[title]{{text-decoration:underline dotted #bbb;text-underline-offset:3px}}
 th:hover{{background:#eef}} th .ar{{color:#c0392b}}
 th:first-child,td.m{{text-align:left}}
 figure{{margin:1.1rem 0}} svg{{width:100%;height:auto;border:1px solid #ddd;background:#fff}}
 figcaption{{color:#666;font-size:.85rem;margin-top:.3rem}}
 .ct{{font:600 13px Georgia,serif;fill:#1a1a1a}} .grid{{stroke:#eee}}
 .ax{{font:10px ui-monospace,monospace;fill:#888}} .leg{{font:10px ui-monospace,monospace}}
 ul,ol{{padding-left:1.2rem}} li{{margin:.2rem 0}}
 .fn{{font-size:.85rem;color:#666;border-top:1px solid #ddd;margin-top:2.4rem;padding-top:.6rem}}
</style></head><body>

<h1>Selecting an embedding model for Harlequin document ingestion: a single-embedder RAG study</h1>
<div class="meta">Corpus: consolidated Treaty on European Union (<code>CELEX:12016M/TXT</code>).
Five candidate embedders, each the sole model on the whole path. Generated from <code>r3_*</code> results.</div>

<div class="abstract"><b>Abstract.</b> {abstract}</div>

<h2>1. Goal &amp; setup</h2>
<p>Harlequin ingests documents, chunks them, embeds the chunks into a SQLite +
sqlite-vec (cosine) store, and retrieves them for memory and RAG. We pick the
single best <code>embedder/chunker/lexical_backend</code> for that path. Each of
the five models is served alone on <code>:2235</code> (llama.cpp, OpenAI API) and
does <i>everything</i> &mdash; semantic boundaries, chunk vectors and query
vectors &mdash; so no result mixes models. Chunk budgets are measured in each
model's own tokens. Questions (802 in-document, grounded to exact sentence ids;
122 misspelled, 100 naive; plus 201 out-of-domain) and the duplicate-aware,
size-normalised scoring are reused unchanged from the prior study.</p>

{body}

<h2>{10 if C2 else 9}. Conclusion</h2>
<p>{('For Harlequin document ingestion, adopt <code>' + esc(win['label']) + '</code>: '
     'it leads the pinpoint+rejection composite at dimension ' + str(win['dim']) + '. '
     'Index small, semantically- or mechanically-bounded chunks. A lexical arm was '
     'not needed for the winner (dense alone beat both RRF hybrids on this corpus); '
     'where exact-token matching does help, custom BM25 and SQLite FTS5 are '
     'interchangeable, so FTS5 is the dependency-free choice. Prompt mode (native '
     'vs no-prefix) and the semantic gate are set per the carried decisions above '
     '&mdash; note that only snowflake&rsquo;s query prefix helped; the other '
     'asymmetric models did best on raw text, matching Harlequin&rsquo;s current '
     'no-prefix embedding.') if win else 'Pending full results.'}</p>

<p class="fn">All figures/tables generated by <code>make_paper3.py</code> from
<code>data/r3_agg.json</code>. Pipeline: <code>r3_server.py</code> (model lifecycle),
<code>r3_build.py</code> (indexes), <code>r3_eval.py</code> (metrics),
<code>r3_aggregate.py</code> (decisions), <code>r3_run.py</code> (orchestration).</p>
<script>
document.querySelectorAll('table.sortable').forEach(function(table){{
  var heads=table.tHead.rows[0].cells;
  for(var i=0;i<heads.length;i++)(function(th){{
    th.addEventListener('click',function(){{
      var tbody=table.tBodies[0],rows=Array.prototype.slice.call(tbody.rows);
      var idx=Array.prototype.indexOf.call(th.parentNode.cells,th);
      var asc=th.getAttribute('data-asc')!=='true';
      Array.prototype.forEach.call(heads,function(h){{h.removeAttribute('data-asc');
        var a=h.querySelector('.ar');if(a)a.remove();}});
      th.setAttribute('data-asc',asc);
      var num=rows.every(function(r){{var t=r.cells[idx].textContent.trim();
        return t===''||t==='-'||!isNaN(parseFloat(t));}});
      rows.sort(function(a,b){{
        var x=a.cells[idx].textContent.trim(),y=b.cells[idx].textContent.trim();
        if(num){{return (asc?1:-1)*((parseFloat(x)||0)-(parseFloat(y)||0));}}
        return asc?x.localeCompare(y):y.localeCompare(x);
      }});
      rows.forEach(function(r){{tbody.appendChild(r);}});
      var s=document.createElement('span');s.className='ar';s.textContent=asc?' ▲':' ▼';
      th.appendChild(s);
    }});
  }})(heads[i]);
}});
</script>
</body></html>"""
    with open(os.path.join(HERE, "paper3.html"), "w") as f:
        f.write(html)
    print("wrote paper3.html")


if __name__ == "__main__":
    main()
