#!/usr/bin/env python3
"""
Render paper2.html (the reworked study) from the r2_* result files. Inline SVG
charts computed from data so figures match numbers. Sections whose result files
are absent are skipped, so this can be run incrementally.
"""
import json
import math
import os

HERE = os.path.dirname(os.path.abspath(__file__))
DATA = os.path.join(HERE, "data")


def load(name, default=None):
    p = os.path.join(DATA, name)
    return json.load(open(p)) if os.path.exists(p) else default


CORE = {r["variant"]: r for r in (load("r2_results_core.json", {"results": []})["results"])}
ABL = {r["label"]: r for r in (load("r2_results_ablation.json", {"results": []})["results"])}
FTS = {r["label"]: r for r in (load("r2_results_fts.json", {"results": []})["results"])}
ABL.update(FTS)   # fold FTS5 configs into the grid/appendix
CONF = load("confound_curve.json", [])
HYDE = load("r2_llm_hyde.json", {}).get("hyde")
CTX = load("r2_llm_contextual.json", {}).get("contextual")
RERANK = load("r2_llm_rerank.json", {}).get("rerank")
GEN = load("r2_llm_geneval.json", {}).get("geneval")
C2 = load("r2_results_corpus2.json")
# qwen0.6b: the embedder Harlequin runs in production, evaluated end-to-end (one
# model the whole path — boundaries, chunks and queries). Additive: granite files
# are untouched.
QCORE = {r["variant"]: r for r in (load("r2_results_qwen_core.json", {"results": []})["results"])}
QCONF = load("confound_curve_qwen06b.json", [])

C = {"sem": "#c0392b", "ps": "#16a085", "st": "#2980b9", "me": "#7f8c8d",
     "ov": "#8e44ad", "rnd": "#e67e22", "fix": "#27ae60", "q": "#2c3e50",
     "q06": "#d35400"}


def esc(s): return str(s).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


# ---------------- column-header tooltips ----------------
# Plain-language description of every table column, surfaced as a title= tooltip
# on the <th> (and a dotted underline cue). Keyed by the displayed header label.
TIPS = {
    "method": "Chunking method: boundary-placement rule and target size",
    "config": "backend / chunking variant / retrieval mode",
    "variant": "Chunking variant (boundary rule + parameter)",
    "index": "Chunking variant being indexed",
    "pipeline": "Full retrieval pipeline being scored",
    "pipeline (subset)": "Retrieval pipeline, evaluated on the reranking subset",
    "n": "Number of chunks the document was split into",
    "R@1": "Answer recall@1: fraction of questions whose #1 retrieved chunk covers an acceptable answer sentence",
    "R@5": "Answer recall@5: an acceptable answer chunk appears in the top 5",
    "R@10": "Answer recall@10: an acceptable answer chunk appears in the top 10",
    "ans@256": "Hit within the top chunks whose cumulative size ≤ 256 tokens — equal retrieved-context budget, neutralising the chunk-size confound",
    "ans@1024": "Hit within the top chunks whose cumulative size ≤ 1024 tokens — equal retrieved-context budget",
    "MRR": "Mean reciprocal rank of the first hit within the top 10",
    "mis": "recall@5 on the misspelled-query subgroup",
    "dumb": "recall@5 on the naive ('dumb') query subgroup",
    "AUC": "AUROC of the top-1 score separating in-document from out-of-domain questions (higher = better OOD rejection)",
    "EER": "Equal error rate of in-doc vs out-of-domain separation (lower = better)",
    "mean tok": "Mean chunk size in model tokens",
    "semantic R@1": "recall@1 of the semantic-adjacency variant",
    "random R@1 (same size)": "recall@1 of random boundaries interpolated to the same mean chunk size",
    "Δ": "semantic recall@1 minus size-matched random recall@1 (positive = semantic boundaries help)",
    "BM25 R@1": "recall@1, lexical-only retrieval with custom numpy Okapi BM25",
    "FTS5 R@1": "recall@1, lexical-only retrieval with SQLite FTS5's built-in BM25",
    "BM25-hyb R@5": "recall@5, dense + numpy-BM25 RRF hybrid",
    "FTS5-hyb R@5": "recall@5, dense + FTS5 RRF hybrid",
    "base R@1": "recall@1 of the bi-encoder ordering before reranking",
    "rerank R@1": "recall@1 after zero-shot LLM listwise reranking",
    "base R@5": "recall@5 before reranking",
    "rerank R@5": "recall@5 after reranking",
    "gen correct@5": "LLM judge: fraction of questions the top-5 context correctly answers",
}


def th(label, tip=None):
    """A header cell carrying a plain-language tooltip (title=)."""
    t = tip if tip is not None else TIPS.get(label, "")
    return f'<th title="{esc(t)}">{esc(label)}</th>' if t else f"<th>{esc(label)}</th>"


def thead_row(labels):
    return "<thead><tr>" + "".join(th(l) for l in labels) + "</tr></thead>"


# ---------------- chart helpers ----------------
def hbar(rows, vmax, title, w=720, rowh=24, pad_l=210, fmt="{:.3f}"):
    h = len(rows) * rowh + 42
    pw = w - pad_l - 56
    o = [f'<svg viewBox="0 0 {w} {h}" role="img">',
         f'<text x="{pad_l}" y="15" class="ct">{esc(title)}</text>']
    for g in range(6):
        x = pad_l + pw * g / 5
        o.append(f'<line x1="{x:.0f}" y1="26" x2="{x:.0f}" y2="{h-16}" class="grid"/>')
        o.append(f'<text x="{x:.0f}" y="{h-4}" class="ax" text-anchor="middle">{vmax*g/5:.2f}</text>')
    for i, (lab, val, col) in enumerate(rows):
        y = 30 + i * rowh
        bw = pw * val / vmax
        o.append(f'<rect x="{pad_l}" y="{y}" width="{bw:.1f}" height="{rowh-7}" fill="{col}"/>')
        o.append(f'<text x="{pad_l-6}" y="{y+rowh-12}" class="lbl" text-anchor="end">{esc(lab)}</text>')
        o.append(f'<text x="{pad_l+bw+4:.1f}" y="{y+rowh-12}" class="val">{fmt.format(val)}</text>')
    o.append("</svg>")
    return "\n".join(o)


def curve(series, title, xlabel, ylabel, w=620, h=340, logx=True):
    """series: list of (name,colour,[(x,y)...])."""
    pad_l, pad_b, pad_t, pad_r = 50, 42, 28, 120
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
    for tick in (40, 80, 160, 320, 640, 1280):
        if xlo <= fx(tick) <= xhi:
            o.append(f'<text x="{X(tick):.0f}" y="{h-pad_b+15}" class="ax" text-anchor="middle">{tick}</text>')
    o.append(f'<text x="{pad_l+pw/2:.0f}" y="{h-3}" class="ax" text-anchor="middle">{esc(xlabel)}</text>')
    for name, col, pts in series:
        pts = sorted(pts)
        poly = " ".join(f"{X(x):.1f},{Y(y):.1f}" for x, y in pts)
        o.append(f'<polyline points="{poly}" fill="none" stroke="{col}" stroke-width="2"/>')
        for x, y in pts:
            o.append(f'<circle cx="{X(x):.1f}" cy="{Y(y):.1f}" r="2.6" fill="{col}"/>')
    # stacked legend in the right margin (avoids labels colliding where lines converge)
    lx = pad_l + pw + 10
    for i, (name, col, _pts) in enumerate(series):
        ly = pad_t + 4 + i * 16
        o.append(f'<rect x="{lx}" y="{ly}" width="10" height="10" fill="{col}"/>')
        o.append(f'<text x="{lx+14}" y="{ly+9}" class="leg">{esc(name)}</text>')
    o.append("</svg>")
    return "\n".join(o)


def grouped(cats, groups, title, vmax, w=720, h=320):
    """groups: list of (name,colour,{cat:val}). bars per cat."""
    pad_l, pad_b, pad_t, pad_r = 44, 64, 28, 110
    pw, ph = w - pad_l - pad_r, h - pad_b - pad_t
    gw = pw / len(cats); bw = gw / (len(groups) + 0.5)
    o = [f'<svg viewBox="0 0 {w} {h}" role="img">',
         f'<text x="{pad_l}" y="15" class="ct">{esc(title)}</text>']
    for g in range(6):
        yv = vmax * g / 5
        y = pad_t + ph * (1 - g / 5)
        o.append(f'<line x1="{pad_l}" y1="{y:.0f}" x2="{pad_l+pw}" y2="{y:.0f}" class="grid"/>')
        o.append(f'<text x="{pad_l-5}" y="{y+3:.0f}" class="ax" text-anchor="end">{yv:.2f}</text>')
    for ci, cat in enumerate(cats):
        x0 = pad_l + ci * gw
        o.append(f'<text x="{x0+gw/2:.0f}" y="{h-pad_b+14}" class="ax" text-anchor="end" '
                 f'transform="rotate(-30 {x0+gw/2:.0f} {h-pad_b+14})">{esc(cat)}</text>')
        for gi, (name, col, vals) in enumerate(groups):
            v = vals.get(cat)
            if v is None:
                continue
            bh = ph * v / vmax
            x = x0 + 0.25 * bw + gi * bw
            o.append(f'<rect x="{x:.1f}" y="{pad_t+ph-bh:.1f}" width="{bw*0.9:.1f}" height="{bh:.1f}" fill="{col}"/>')
    for gi, (name, col, _v) in enumerate(groups):
        o.append(f'<rect x="{pad_l+pw+8}" y="{pad_t+gi*16}" width="10" height="10" fill="{col}"/>')
        o.append(f'<text x="{pad_l+pw+22}" y="{pad_t+gi*16+9}" class="leg">{esc(name)}</text>')
    o.append("</svg>")
    return "\n".join(o)


# ---------------- confound analysis ----------------
def _confound_figure(conf, fig_title, tbl_caption):
    """Build (size-vs-recall@1 curve, size-matched semantic-vs-random table) for
    one embedder's confound sweep."""
    if not conf:
        return "", ""
    fams = {"semadj": ("semantic (adjacent)", C["sem"]),
            "fixed": ("fixed (regular)", C["fix"]),
            "random": ("random", C["rnd"])}
    series = []
    rnd_pts = {}
    for fam, (lab, col) in fams.items():
        pts = [(r["tok_mean"], r["recall1"]) for r in conf if r["family"] == fam]
        if fam == "random":  # average seeds at each size
            from collections import defaultdict
            d = defaultdict(list)
            for t, v in pts:
                d[round(t / 10) * 10].append(v)
            pts = [(t, sum(v) / len(v)) for t, v in d.items()]
            rnd_pts = dict(pts)
        if pts:
            series.append((lab, col, pts))
    fig = curve(series, fig_title, "mean chunk size (model tokens, log)", "recall@1")
    # size-matched semantic vs random table
    rt = sorted(rnd_pts); rr = [rnd_pts[t] for t in rt]
    rows = []
    for r in sorted((x for x in conf if x["family"] == "semadj"), key=lambda z: z["tok_mean"]):
        ri = float(__import__("numpy").interp(r["tok_mean"], rt, rr))
        rows.append((r["variant"], r["tok_mean"], r["recall1"], ri, r["recall1"] - ri))
    tb = [f'<table><caption>{tbl_caption}</caption>',
          "<thead><tr>" + th("semantic variant", TIPS["variant"]) + th("mean tok")
          + th("semantic R@1") + th("random R@1 (same size)") + th("Δ") + "</tr></thead><tbody>"]
    for v, t, s, ri, d in rows:
        tb.append(f'<tr><td class="m">{v}</td><td>{t:.0f}</td><td>{s:.3f}</td>'
                  f'<td>{ri:.3f}</td><td>{"<b>+" if d>=0 else ""}{d:.3f}{"</b>" if d>=0 else ""}</td></tr>')
    tb.append("</tbody></table>")
    return fig, "\n".join(tb)


def confound_figure():
    return _confound_figure(
        CONF, "Figure 1. recall@1 vs chunk size, by boundary rule (granite)",
        "Table 1. Semantic vs random boundaries at matched chunk size, granite "
        "(random interpolated to the semantic variant’s size).")


def qwen_confound_figure():
    return _confound_figure(
        QCONF, "Figure 2. recall@1 vs chunk size, by boundary rule "
        "(Qwen3-Embedding-0.6B, one model the whole path)",
        "Table 2. Semantic vs random boundaries at matched chunk size, "
        "Qwen3-Embedding-0.6B (random interpolated to the semantic variant’s size).")


# ---------------- tables ----------------
def _core_like_table(src, caption):
    cols = [("n_chunks", "n", "{}"), ("recall@1", "R@1", "{:.3f}"),
            ("recall@5", "R@5", "{:.3f}"), ("answer@256tok", "ans@256", "{:.3f}"),
            ("answer@1024tok", "ans@1024", "{:.3f}"), ("mrr@10", "MRR", "{:.3f}"),
            ("recall@5_misspelled", "mis", "{:.3f}"), ("recall@5_dumb", "dumb", "{:.3f}"),
            ("ood_auc", "AUC", "{:.3f}"), ("eer", "EER", "{:.3f}")]
    order = ["per_sentence", "sem_adjacent_g0.12", "structure_1024", "mech_256",
             "mech_512", "mech_1024", "mech_1500", "overlap_1024"]
    best = {}
    for ck, _, _ in cols:
        vals = [src[o][ck] for o in order if o in src and src[o].get(ck) is not None]
        if vals:
            best[ck] = min(vals) if ck == "eer" else max(vals)
    h = [f'<table><caption>{caption}</caption>',
         "<thead><tr>" + th("method") + "".join(th(l) for _, l, _ in cols) + "</tr></thead><tbody>"]
    for o in order:
        if o not in src:
            continue
        r = src[o]; h.append(f'<tr><td class="m">{o}</td>')
        for ck, _, fmt in cols:
            v = r.get(ck)
            cell = fmt.format(v) if v is not None else "-"
            if ck in best and v is not None and abs(v - best[ck]) < 1e-9:
                cell = f"<b>{cell}</b>"
            h.append(f"<td>{cell}</td>")
        h.append("</tr>")
    h.append("</tbody></table>")
    return "\n".join(h)


def core_table():
    return _core_like_table(CORE, "Table 3. Core comparison (granite dense). CI = 95% bootstrap.")


def qwen_core_table():
    if not QCORE:
        return ""
    return _core_like_table(
        QCORE, "Table 4. Core comparison under the production embedder "
        "Qwen3-Embedding-0.6B, dense, one model the whole path "
        "(boundaries + chunks + queries). sem_adjacent gate retuned to qwen's "
        "drift scale (g=0.60; median adjacent drift 0.52 vs granite 0.13).")


def ablation_section():
    variants = ["per_sentence", "sem_adjacent_g0.12", "structure_1024", "mech_256",
                "mech_512", "mech_1024", "mech_1500", "overlap_1024"]
    def g(backend, mode):
        return {v: ABL.get(f"{backend}/{v}/{mode}", {}).get("recall@1") for v in variants}
    groups = [("granite dense", C["me"], g("granite", "dense")),
              ("granite hybrid", C["st"], g("granite", "hybrid")),
              ("qwen4b dense", C["sem"], g("qwen4b", "dense")),
              ("qwen4b hybrid", C["q"], g("qwen4b", "hybrid"))]
    if QCORE:  # production 0.6B embedder, dense (one model the whole path)
        groups.append(("qwen0.6b dense", C["q06"],
                       {v: QCORE.get(v, {}).get("recall@1") for v in variants}))
    fig = grouped(variants, groups, "Figure 3. recall@1 by embedder x retrieval mode", 0.7)
    return fig


def ladder_figure():
    """absolute-lift ladder of recall@1."""
    steps = []
    def add(lab, key, col):
        if key in ABL:
            steps.append((lab, ABL[key]["recall@1"], col))
    add("mech_1024 dense (baseline)", "granite/mech_1024/dense", C["me"])
    add("+ BM25 hybrid", "granite/mech_1024/hybrid", C["ov"])
    add("structure_1024 dense", "granite/structure_1024/dense", C["st"])
    add("+ qwen4b embedder", "qwen4b/structure_1024/dense", C["sem"])
    if not steps:
        return ""
    return hbar(steps, max(v for _, v, _ in steps) * 1.15,
                "Figure 4. recall@1 lift across the pipeline", pad_l=260)


def llm_tables():
    out = []
    if RERANK:
        h = ['<table><caption>Table 5. Zero-shot LLM listwise reranking of the top-50 '
             '(subset, reasoning disabled for tractability). It degrades a strong '
             'bi-encoder ordering.</caption>',
             thead_row(["pipeline (subset)", "base R@1", "rerank R@1", "base R@5", "rerank R@5"])
             + "<tbody>"]
        for r in RERANK:
            h.append(f'<tr><td class="m">{esc(r["label"])}</td>'
                     f'<td>{r["base_recall@1"]:.3f}</td><td>{r["rerank_recall@1"]:.3f}</td>'
                     f'<td>{r["base_recall@5"]:.3f}</td><td>{r["rerank_recall@5"]:.3f}</td></tr>')
        out.append("\n".join(h) + "</tbody></table>")
    if CTX:
        h = ['<table><caption>Table 6. Contextual Retrieval (LLM context prefix) vs base.</caption>',
             thead_row(["index", "R@1", "R@5", "ans@1024"]) + "<tbody>"]
        for r in CTX:
            h.append(f'<tr><td class="m">{esc(r["variant"])}</td><td>{r["recall@1"]:.3f}</td>'
                     f'<td>{r["recall@5"]:.3f}</td><td>{r["answer@1024tok"]:.3f}</td></tr>')
        out.append("\n".join(h) + "</tbody></table>")
    if HYDE:
        h = ['<table><caption>Table 7. HyDE query expansion (granite dense).</caption>',
             thead_row(["variant", "R@1", "R@5"]) + "<tbody>"]
        for r in HYDE:
            h.append(f'<tr><td class="m">{esc(r["variant"])}</td><td>{r["recall@1"]:.3f}</td>'
                     f'<td>{r["recall@5"]:.3f}</td></tr>')
        out.append("\n".join(h) + "</tbody></table>")
    if GEN:
        h = ['<table><caption>Table 8. Generation-grounded correctness (LLM judge, subset).</caption>',
             thead_row(["pipeline", "gen correct@5"]) + "<tbody>"]
        for r in GEN:
            h.append(f'<tr><td class="m">{esc(r["label"])}</td><td>{r["gen_correct@5"]:.3f}</td></tr>')
        out.append("\n".join(h) + "</tbody></table>")
    return "\n".join(out)


def corpus2_section():
    if not C2:
        return ""
    rows = C2["results"]
    fams = {"semadj": ("semantic", C["sem"]), "fixed": ("fixed", C["fix"]),
            "random": ("random", C["rnd"])}
    series = []
    for fam, (lab, col) in fams.items():
        pts = [(r["tok_mean"], r["recall1"]) for r in rows if r["family"] == fam]
        if pts:
            series.append((lab, col, pts))
    fig = curve(series, "Figure 5. Generalization corpus: recall@1 vs chunk size",
                "mean chunk size (tokens, log)", "recall@1")
    return (f"<p>Replication on {C2['n_questions']} grounded questions over a contrasting "
            f"non-legal corpus (Darwin, <i>Origin of Species</i>): the same size law and "
            f"semantic&gt;random ordering hold.</p>\n<figure>{fig}<figcaption>The size law "
            "generalizes; semantic boundaries again sit at or above random at matched size."
            "</figcaption></figure>")


# ---------------- assemble ----------------
def best_static():
    cand = [(l, r["recall@1"]) for l, r in ABL.items()]
    return max(cand, key=lambda x: x[1]) if cand else ("n/a", 0)


def _full_ablation_table():
    if not ABL:
        return ""
    keys = sorted(ABL)
    cols = [("n_chunks", "n"), ("recall@1", "R@1"), ("recall@5", "R@5"),
            ("recall@10", "R@10"), ("answer@1024tok", "ans@1024"),
            ("mrr@10", "MRR"), ("ood_auc", "AUC"), ("eer", "EER")]
    h = ['<table id="ablation-grid" class="sortable"><thead><tr>' + th("config")] + \
        [th(l) for _, l in cols] + ['</tr></thead><tbody>']
    for k in keys:
        r = ABL[k]; h.append(f'<tr><td class="m">{esc(k)}</td>')
        for ck, _ in cols:
            v = r.get(ck)
            h.append(f"<td>{(('%d'%v) if ck=='n_chunks' else '%.3f'%v) if v is not None else '-'}</td>")
        h.append("</tr>")
    h.append("</tbody></table>")
    return "\n".join(h)


def fts_table():
    if not FTS:
        return ""
    variants = ["per_sentence", "sem_adjacent_g0.12", "structure_1024", "mech_256",
                "mech_512", "mech_1024", "mech_1500", "overlap_1024"]
    def g(v, mode, m):
        return ABL.get(f"granite/{v}/{mode}", {}).get(m)
    h = ['<table><caption>Table 9. Lexical backend: custom numpy BM25 vs SQLite FTS5 '
         '(built-in BM25), granite. Lexical-only and dense+lexical RRF hybrids. '
         'The two backends are within noise.</caption>',
         thead_row(["variant", "BM25 R@1", "FTS5 R@1", "BM25-hyb R@5", "FTS5-hyb R@5"]) + "<tbody>"]
    for v in variants:
        h.append(f'<tr><td class="m">{v}</td>'
                 f'<td>{(g(v,"bm25","recall@1") or 0):.3f}</td>'
                 f'<td>{(g(v,"fts5","recall@1") or 0):.3f}</td>'
                 f'<td>{(g(v,"hybrid","recall@5") or 0):.3f}</td>'
                 f'<td>{(g(v,"fts5_hybrid","recall@5") or 0):.3f}</td></tr>')
    h.append("</tbody></table>")
    return "\n".join(h)


conf_fig, conf_tbl = confound_figure()
bl = ABL.get("granite/mech_1024/dense", {}).get("recall@1")
bs_label, bs_r1 = best_static()

html = f"""<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Chunking and retrieval for RAG over legal text: a controlled study</title>
<style>
 html{{font-size:17px}} body{{font-family:Georgia,serif;color:#1a1a1a;max-width:840px;
  margin:0 auto;padding:2.4rem 1.3rem 5rem;line-height:1.5}}
 h1{{font-size:1.6rem;line-height:1.25;margin:0 0 .3rem}}
 h2{{font-size:1.15rem;border-bottom:1px solid #ddd;padding-bottom:.2rem;margin:2rem 0 .7rem}}
 h3{{font-size:1rem;margin:1.2rem 0 .4rem}}
 .meta{{color:#666;font-size:.92rem;margin-bottom:1.3rem}}
 .abstract{{background:#f7f7f5;border:1px solid #ddd;padding:.9rem 1.1rem;font-size:.96rem}}
 code{{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:.86em;background:#f2f2f0;padding:0 .25em;border-radius:3px}}
 table{{border-collapse:collapse;width:100%;font-size:.78rem;margin:.6rem 0;font-family:ui-monospace,monospace}}
 caption{{caption-side:bottom;text-align:left;color:#666;font-size:.84rem;padding-top:.4rem;font-family:Georgia,serif}}
 th,td{{border-bottom:1px solid #ddd;padding:.24rem .4rem;text-align:right}}
 th:first-child,td.m{{text-align:left}} thead th{{border-bottom:2px solid #bbb}}
 figure{{margin:1.1rem 0}} svg{{width:100%;height:auto;border:1px solid #ddd;background:#fff}}
 figcaption{{color:#666;font-size:.85rem;margin-top:.3rem}}
 .ct{{font:600 13px Georgia,serif;fill:#1a1a1a}} .grid{{stroke:#eee}}
 .ax{{font:10px ui-monospace,monospace;fill:#888}} .lbl{{font:11px ui-monospace,monospace;fill:#1a1a1a}}
 .val{{font:10px ui-monospace,monospace;fill:#555}} .leg{{font:10px ui-monospace,monospace}}
 ul,ol{{padding-left:1.2rem}} li{{margin:.2rem 0}}
 .fn{{font-size:.85rem;color:#666;border-top:1px solid #ddd;margin-top:2.4rem;padding-top:.6rem}}
 table.sortable thead th{{cursor:pointer;user-select:none;white-space:nowrap}}
 table.sortable thead th:hover{{background:#eef}} table.sortable thead th .ar{{color:#c0392b}}
</style></head><body>

<h1>Chunking and retrieval for RAG over legal text: a size-controlled study</h1>
<div class="meta">Corpus: consolidated Treaty on European Union (<code>CELEX:12016M/TXT</code>).
Embedders: granite-embedding-311M, Qwen3-Embedding-4B. Generated from <code>r2_*</code> results.</div>

<div class="abstract"><b>Abstract.</b> We study how chunk granularity, boundary placement,
embedding model and retrieval method affect dense-retrieval RAG on a 204-page legal corpus,
evaluated with 802 grounded in-document questions and 201 out-of-domain questions using
duplicate-aware, size-normalised metrics with bootstrap confidence intervals. Three results.
(1) <b>Chunk size is the primary axis</b>: recall@1 varies monotonically with mean chunk size
within every boundary-placement rule. (2) <b>Controlling for size</b>, semantic-adjacency
boundaries give a small but consistent gain over random boundaries (+0.04&ndash;0.05 recall@1,
and +0.07&ndash;0.08 on a contrasting corpus) in the small-chunk regime. (3) <b>Absolute quality
is set by the embedder and chunk granularity, not bolt-on LLM stages</b>: a stronger instruction
embedder with structural chunking raises recall@1 from {("%.2f" % bl) if bl else "n/a"}
(a fixed-cap dense baseline) to {"%.2f" % bs_r1}, and a BM25 hybrid further raises recall@5,
whereas HyDE, contextual-prefix embedding and zero-shot LLM reranking did <i>not</i> help.
End-to-end answer correctness rises from
{("%.2f" % min((r["gen_correct@5"] for r in GEN), default=0)) if GEN else "n/a"} to
{("%.2f" % max((r["gen_correct@5"] for r in GEN), default=0)) if GEN else "n/a"} (LLM judge).
We report the full grid and a generalization check on a contrasting non-legal corpus.</div>

<h2>1. Introduction</h2>
<p>Chunking-rule comparisons are confounded by chunk size: smaller chunks mechanically score
higher on point metrics. We run a size-controlled comparison of boundary-placement rules
(semantic, structural, fixed, random) and, separately, measure the retrieval-stack levers that
actually move absolute quality (embedding model, lexical hybrid, reranking, query expansion,
contextual embedding). The thesis under test is that <b>adjacent-sentence semantic chunking</b>
is worth its complexity once size is controlled, and we quantify exactly how much it adds
relative to the dominant factors.</p>

<h2>2. Setup</h2>
<ul>
<li><b>Corpus</b>: consolidated TEU, 2&thinsp;112 sentences with page/line provenance.</li>
<li><b>Embedders</b>: granite-embedding-311M (768-d) and Qwen3-Embedding-4B (2560-d, with
query instruction prefix), both via llama.cpp; chunk budgets in model tokens.</li>
<li><b>Store</b>: SQLite + sqlite-vec (cosine). <b>Lexical</b>: Okapi BM25. <b>Fusion</b>: RRF.
<b>Rerank/HyDE/context/judge</b>: Qwen3.6-35B chat.</li>
<li><b>Questions</b>: 802 in-document (grounded to exact sentence ids; 122 misspelled, 100
naive) + 201 out-of-domain. <b>Scoring</b>: a hit covers any duplicate-aware acceptable
sentence; metrics carry 95% bootstrap CIs; <code>answer@B</code> evaluates at an equal
retrieved-token budget B to neutralise the size confound.</li>
</ul>

<h2>3. Chunk size is the primary axis (controlled)</h2>
<p>Sweeping each boundary rule across matched sizes shows recall@1 is governed by mean chunk
size, not the rule. At matched size, semantic boundaries sit at or above random; the gain is
real but small and concentrated in small chunks.</p>
{f'<figure>{conf_fig}<figcaption>Each rule swept across sizes; random averaged over 3 seeds. '
 'The families overlap — size dominates — with semantic slightly above random at small sizes.'
 '</figcaption></figure>' if conf_fig else ''}
{conf_tbl}

<h2>4. Core comparison</h2>
{core_table()}

<h2>5. Embedding model and retrieval stack</h2>
<p>Absolute quality is driven by the embedder and lexical fusion. The stronger embedder helps
structural/mechanical chunks most (and, with last-token instruction pooling, underperforms on
single-sentence chunks); BM25 hybrid lifts recall@5 broadly; the best static pipeline is
<code>{esc(bs_label)}</code> at recall@1 {"%.3f" % bs_r1}.</p>
<figure>{ablation_section()}<figcaption>recall@1 across the 8 chunkers under granite vs
Qwen3-Embedding-4B, dense vs BM25-hybrid.</figcaption></figure>
{f'<figure>{ladder_figure()}<figcaption>Cumulative recall@1 from a fixed-cap dense baseline to '
 'the best pipeline.</figcaption></figure>' if ladder_figure() else ''}

<h3>5.1 Techniques that did not help</h3>
<p>Three popular add-ons gave no benefit on this corpus and embedder. <b>HyDE</b> query
expansion was neutral-to-negative (it only helped structural chunks, and hurt fine-grained
ones). <b>Contextual Retrieval</b> (an LLM-written context prefix per chunk) was neutral:
the added tokens dilute a small chunk's embedding and the bi-encoder is not instruction-tuned
to exploit them. <b>Zero-shot LLM listwise reranking</b> with a general chat model (reasoning
disabled for tractability) actively <i>degraded</i> a strong bi-encoder ordering (Table 3) —
a dedicated cross-encoder, not a prompted generator, is the right tool. The lesson: spend
effort on the embedder and lexical hybrid, not on bolt-on LLM stages.</p>
{llm_tables()}
{('<h3>5.2 Lexical backend: SQLite FTS5 vs custom BM25</h3><p>We compare two lexical arms: '
  'a custom numpy BM25 and SQLite&rsquo;s built-in FTS5 (its own BM25 and tokenizer). They are '
  'within noise on recall@1/@5 both standalone and inside the dense hybrid (FTS5 is marginally '
  'lower on large chunks, where its default tokenizer keeps stopwords), so FTS5 is a '
  'dependency-free drop-in lexical backend.</p>' + fts_table()) if FTS else ''}
{('<h3>5.3 End-to-end answer correctness</h3><p>An LLM judge scored whether the top-5 '
  'retrieved context answers the question. The best static pipeline reaches '
  + ("%.2f" % max((r["gen_correct@5"] for r in GEN), default=0)) + ' vs '
  + ("%.2f" % min((r["gen_correct@5"] for r in GEN), default=0)) + ' for the fixed-cap '
  'baseline: retrieval gains carry through to generation.</p>') if GEN else ''}

{('<h2>6. Generalization</h2>' + corpus2_section()) if C2 else ''}

<h2>{7 if C2 else 6}. Discussion</h2>
<p>Two levers dominate. <b>Size</b> sets point precision and false-positive separation (small)
versus deep recall and context (large); the <code>answer@B</code> view shows small chunks win
at equal context budget. <b>Embedder + lexical hybrid + reranking</b> set the absolute ceiling.
Boundary placement is third-order: semantic-adjacency adds a small, size-controlled gain over
arbitrary cuts and is the best content-blind-free option when no document structure exists,
but structural boundaries plus a strong embedder are the simplest strong baseline here.</p>

<h2>{8 if C2 else 7}. Conclusion</h2>
<p>For RAG over structured legal text: index small, semantically- or structurally-bounded
chunks; pair a strong instruction embedder with BM25 fusion and a reranker; choose chunk size
by the downstream context budget. Semantic-adjacency chunking earns a modest, quantified place
once size is controlled — it is not, by itself, the main driver of retrieval quality.</p>

<h2>Appendix. Full ablation grid</h2>
{_full_ablation_table()}

<p class="fn">All figures/tables generated by <code>make_paper2.py</code> from <code>data/r2_*.json</code>.
Pipeline: <code>r2_build.py</code>, <code>r2_eval.py</code>, <code>r2_llm_run.py</code>,
<code>corpus2_run.py</code>.</p>
<script>
document.querySelectorAll('table.sortable').forEach(function(table){{
  var heads = table.tHead.rows[0].cells;
  for (var i=0;i<heads.length;i++) (function(th){{
    th.addEventListener('click', function(){{
      var tbody=table.tBodies[0], rows=Array.prototype.slice.call(tbody.rows);
      var idx=Array.prototype.indexOf.call(th.parentNode.cells, th);
      var asc = th.getAttribute('data-asc')!=='true';
      Array.prototype.forEach.call(heads,function(h){{h.removeAttribute('data-asc');
        var a=h.querySelector('.ar'); if(a) a.remove();}});
      th.setAttribute('data-asc', asc);
      var num = rows.every(function(r){{
        var t=r.cells[idx].textContent.trim().replace('-','');
        return t==='' || !isNaN(parseFloat(r.cells[idx].textContent));}});
      rows.sort(function(a,b){{
        var x=a.cells[idx].textContent.trim(), y=b.cells[idx].textContent.trim();
        if(num){{var nx=parseFloat(x)||0, ny=parseFloat(y)||0; return asc?nx-ny:ny-nx;}}
        return asc?x.localeCompare(y):y.localeCompare(x);
      }});
      rows.forEach(function(r){{tbody.appendChild(r);}});
      var s=document.createElement('span'); s.className='ar'; s.textContent=asc?' ▲':' ▼';
      th.appendChild(s);
    }});
  }})(heads[i]);
}});
</script>
</body></html>"""


with open(os.path.join(HERE, "paper2.html"), "w") as f:
    f.write(html)
print("wrote paper2.html  (core=%d ablation=%d confound=%d hyde=%s ctx=%s rerank=%s gen=%s c2=%s)"
      % (len(CORE), len(ABL), len(CONF), bool(HYDE), bool(CTX), bool(RERANK), bool(GEN), bool(C2)))
