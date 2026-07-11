package agent

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ivoras/harlequin/internal/server/docalign"
	"github.com/ivoras/harlequin/internal/server/documents"
)

// align_docs limits: a pairs batch stops after batchMaxPairs pairs or once
// batchMaxRunes of section text has been rendered (whichever first), so one
// tool result stays small enough for the model to analyse pair by pair. A
// single side is clipped at sideMaxRunes. Summary lists are capped so an
// 80-page regulation still fits one result.
const (
	alignBatchMaxPairs   = 4
	alignBatchMaxRunes   = 6000
	alignSideMaxRunes    = 2400
	alignDefaultMinSim   = 0.55
	alignSummaryChanged  = 50 // max changed/matched lines in the summary
	alignSummaryOrphans  = 40 // max headings per only_a/only_b summary list
	alignMinUnitsForUnit = 4  // fewer units than this on a side → chunk-level mode
	// alignExplicitMaxPairs is the per-call cap when the model names specific
	// pair numbers (pairs=): higher than the cursor-walk cap because the model
	// already curated the list by reading the summary — one thematic question
	// (e.g. "what changed about funding", touching 15-20 headings) should cost
	// a handful of calls, not one per pair. The rune budget still applies, so
	// long sections still page correctly.
	alignExplicitMaxPairs = 12
	// alignMaxSelectable caps how many pair numbers one pairs= call accepts, so
	// a runaway list (e.g. the model pasting the whole summary) can't demand an
	// unbounded response.
	alignMaxSelectable = 60
)

// alignDocsEntry is the align_docs tool: deterministic two-document alignment
// served to the model as a structural summary plus drill-down pair batches.
func (a *Agent) alignDocsEntry() toolEntry {
	return toolEntry{
		def: fnTool("align_docs", `Align two documents from the corpus so their differences can be analysed. Use mode "versions" for two revisions of the same text and mode "topical" for two different texts about the same subject. Documents are aligned at section level (e.g. per article), anchored on section headings; unchanged sections are skipped.
Documents are referenced by scoped id: u.N (personal), s.N (shared), p.N (project), p<project>.N (a specific project, e.g. p3.17 from an all-projects search), exactly as list_documents and search_docs show them; a bare N works when it is unambiguous.
Call it first WITHOUT view to get the summary: which sections changed (most-different first), and which exist in only one document, each with a pair number "#N". For a narrow question (one article, one specific change), read the 1-4 most relevant pairs with view="pairs" and cursor=<#N> or filter="<keyword>". For a THEMATIC question spanning many sections (e.g. "what changed about funding", "how did oversight change", "which bodies were added/removed") — read every heading in the summary, pick every pair number whose heading plausibly relates to the theme (this can be 10-30+ pairs, do not under-select), and fetch them all with pairs="<comma-separated numbers, e.g. 9,12,16,19-22,44>" — one or a few pairs= calls, NOT one call per pair. filter also accepts several comma-separated terms (OR-matched) to gather a theme by keyword instead of by number. Analyse each returned pair (state the substantive difference, or that there is none) before fetching more. The alignment is deterministic: the same arguments always produce the same pairs and numbering.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"doc_a": map[string]any{"type": "string", "description": "First document, e.g. \"p.6\" (in versions mode: the OLD revision)"},
				"doc_b": map[string]any{"type": "string", "description": "Second document (versions mode: the NEW revision)"},
				"mode":  map[string]any{"type": "string", "enum": []string{"versions", "topical"}},
				"view":  map[string]any{"type": "string", "enum": []string{"summary", "pairs"}, "description": "summary (default): structural overview; pairs: full text of aligned pairs"},
				"cursor": map[string]any{"type": "integer",
					"description": "view=pairs: 1-based pair number to start from (numbers come from the summary and batch footers). Ignored if pairs is set."},
				"pairs": map[string]any{"type": "string",
					"description": "view=pairs: explicit pair numbers/ranges to fetch together, e.g. \"9,12,16,19-22,44\" — use for a thematic question touching many sections, chosen from the summary. Takes priority over cursor."},
				"filter": map[string]any{"type": "string",
					"description": "view=pairs: only pairs whose heading or text contains one of these terms (case-insensitive, comma-separated for OR), e.g. \"grant rate\" or \"grant,subsidy,funding,co-financing\""},
				"min_similarity": map[string]any{"type": "number",
					"description": "Cosine floor for pairing sections without a common heading (default 0.55)"},
			},
			"required": []string{"doc_a", "doc_b", "mode"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			mode := argString(args, "mode")
			if mode != "versions" && mode != "topical" {
				return `error: mode must be "versions" or "topical"`, nil
			}
			minSim := argFloat(args, "min_similarity", alignDefaultMinSim)
			if minSim <= 0 || minSim >= 1 {
				return "error: min_similarity must be between 0 and 1 (exclusive)", nil
			}
			docA, errMsg := a.loadDocRef(ctx, rc, argString(args, "doc_a"))
			if errMsg != "" {
				return errMsg, nil
			}
			docB, errMsg := a.loadDocRef(ctx, rc, argString(args, "doc_b"))
			if errMsg != "" {
				return errMsg, nil
			}
			if len(docA.Sections) == 0 || len(docB.Sections) == 0 {
				return "error: both documents must have indexed sections (one of them has none)", nil
			}
			dbA, errMsg := a.docDBForScope(ctx, rc, docA.Scope)
			if errMsg != "" {
				return errMsg, nil
			}
			dbB, errMsg := a.docDBForScope(ctx, rc, docB.Scope)
			if errMsg != "" {
				return errMsg, nil
			}

			res := docalign.AlignUnits(docA, docB, mode, minSim)
			if res.UnitsA < alignMinUnitsForUnit || res.UnitsB < alignMinUnitsForUnit {
				// Unstructured documents (no usable headings): chunk-level alignment.
				return a.alignChunksFallback(docA, docB, mode, minSim, args)
			}

			view := argString(args, "view")
			if view == "" {
				view = "summary"
			}
			header := renderUnitHeader(docA, docB, res, minSim)
			if view == "summary" {
				return header + renderUnitSummary(res), nil
			}
			cursor := argInt(args, "cursor", 1)
			if cursor < 1 {
				cursor = 1
			}
			pairsSpec := strings.TrimSpace(argString(args, "pairs"))
			if pairsSpec != "" {
				nums, err := parsePairSpec(pairsSpec, len(res.Pairs), alignMaxSelectable)
				if err != "" {
					return "error: " + err, nil
				}
				return header + a.renderUnitPairsExplicit(ctx, docA, dbA, docB, dbB, res, nums), nil
			}
			return header + a.renderUnitPairs(ctx, docA, dbA, docB, dbB, res, cursor, strings.TrimSpace(argString(args, "filter"))), nil
		},
	}
}

// alignChunksFallback is the pre-unit chunk-level alignment, for documents
// without heading structure (short notes, plain prose).
func (a *Agent) alignChunksFallback(docA, docB *docalign.Doc, mode string, minSim float64, args map[string]any) (string, error) {
	var res *docalign.Result
	if mode == "versions" {
		res = docalign.AlignVersions(docA, docB, minSim)
	} else {
		var err error
		res, err = docalign.AlignTopical(docA, docB, minSim)
		if err != nil {
			return "error: " + err.Error(), nil
		}
	}
	cursor := argInt(args, "cursor", 1)
	if cursor < 1 {
		cursor = 1
	}
	return renderAlignment(docA, docB, res, mode, minSim, cursor), nil
}

func renderUnitHeader(docA, docB *docalign.Doc, res *docalign.UnitResult, minSim float64) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Aligned %q (%s, %d sections) vs %q (%s, %d sections), mode=%s, min_similarity=%.2f.\n",
		docA.Title, docRef(docA), res.UnitsA, docB.Title, docRef(docB), res.UnitsB, res.Mode, minSim)
	c := res.Counts()
	paired := pairedKind(res.Mode)
	fmt.Fprintf(&sb, "%d identical sections skipped. %d pairs: %d %s, %d only_a (only in A), %d only_b (only in B).\n",
		res.Identical, len(res.Pairs), c[paired], paired, c[docalign.OnlyA], c[docalign.OnlyB])
	return sb.String()
}

// renderUnitSummary lists the alignment structurally: paired sections most
// different first, then per-side orphan headings — all numbered for drill-down.
func renderUnitSummary(res *docalign.UnitResult) string {
	if len(res.Pairs) == 0 {
		if res.Mode == "versions" {
			return "No differences found: the documents are identical section for section."
		}
		return "No sections were paired or left over (try a lower min_similarity)."
	}
	type numbered struct {
		n int
		p docalign.UnitPair
	}
	var paired, onlyA, onlyB []numbered
	for i, p := range res.Pairs {
		switch p.Kind {
		case docalign.OnlyA:
			onlyA = append(onlyA, numbered{i + 1, p})
		case docalign.OnlyB:
			onlyB = append(onlyB, numbered{i + 1, p})
		default:
			paired = append(paired, numbered{i + 1, p})
		}
	}
	// Most different first: these are what a "what changed" answer leads with.
	for i := 0; i < len(paired); i++ {
		for j := i + 1; j < len(paired); j++ {
			if paired[j].p.Similarity < paired[i].p.Similarity {
				paired[i], paired[j] = paired[j], paired[i]
			}
		}
	}
	var sb strings.Builder
	if len(paired) > 0 {
		fmt.Fprintf(&sb, "\nPaired sections that differ (most different first; #N = pair number for view=\"pairs\"):\n")
		for i, e := range paired {
			if i >= alignSummaryChanged {
				fmt.Fprintf(&sb, "… and %d more pairs with higher similarity (mostly minor differences).\n", len(paired)-i)
				break
			}
			ha, hb := unitHeading(e.p.A), unitHeading(e.p.B)
			if ha == hb {
				fmt.Fprintf(&sb, "#%d [sim %.2f] %s\n", e.n, e.p.Similarity, ha)
			} else {
				fmt.Fprintf(&sb, "#%d [sim %.2f] A: %s ↔ B: %s\n", e.n, e.p.Similarity, ha, hb)
			}
		}
	}
	writeOrphans := func(label string, list []numbered) {
		if len(list) == 0 {
			return
		}
		fmt.Fprintf(&sb, "\n%s (%d):\n", label, len(list))
		for i, e := range list {
			if i >= alignSummaryOrphans {
				fmt.Fprintf(&sb, "… and %d more.\n", len(list)-i)
				break
			}
			u := e.p.A
			if u == nil {
				u = e.p.B
			}
			fmt.Fprintf(&sb, "#%d %s\n", e.n, unitHeading(u))
		}
	}
	writeOrphans("Only in A", onlyA)
	writeOrphans("Only in B", onlyB)
	sb.WriteString("\nTo read pairs: align_docs with view=\"pairs\" and cursor=<#N> (one, walking forward) or filter=\"<keyword>\" (single topic). For a THEME spanning many headings, instead pick every relevant #N from above and pass them together as pairs=\"9,12,16,...\" — do not fetch a themed set one pair per call.")
	return sb.String()
}

// renderUnitPairs prints full pair texts from the 1-based cursor, optionally
// restricted to pairs mentioning filter.
func (a *Agent) renderUnitPairs(ctx context.Context, docA *docalign.Doc, dbA *sql.DB, docB *docalign.Doc, dbB *sql.DB, res *docalign.UnitResult, cursor int, filter string) string {
	type numbered struct {
		n int
		p docalign.UnitPair
	}
	var sel []numbered
	low := strings.ToLower(filter)
	for i, p := range res.Pairs {
		if filter != "" && !unitPairMatches(p, low) {
			continue
		}
		sel = append(sel, numbered{i + 1, p})
	}
	var sb strings.Builder
	if filter != "" {
		fmt.Fprintf(&sb, "%d pairs mention %q.\n", len(sel), filter)
	}
	if len(sel) == 0 {
		sb.WriteString("No pairs match — try another filter, or view=\"summary\" for the overview.")
		return sb.String()
	}
	// The cursor is a global pair number; start at the first selected pair >= it.
	start := 0
	for start < len(sel) && sel[start].n < cursor {
		start++
	}
	if start >= len(sel) {
		fmt.Fprintf(&sb, "Cursor %d is past the last matching pair: all pairs have been retrieved. Write your overall analysis now.", cursor)
		return sb.String()
	}
	// A lone pair (typical for a filtered drill-down) gets the whole batch
	// budget: clipping a side hides paragraphs and misleads the analysis into
	// "X was added" claims when X merely fell past the clip.
	sideMax := alignSideMaxRunes
	if len(sel)-start == 1 {
		sideMax = alignBatchMaxRunes * 4 / 5
	}
	runes := 0
	last := start
	var body strings.Builder
	for i := start; i < len(sel); i++ {
		if i >= start+alignBatchMaxPairs || (i > start && runes >= alignBatchMaxRunes) {
			break
		}
		e := sel[i]
		var pb strings.Builder
		fmt.Fprintf(&pb, "\n### Pair #%d — %s", e.n, e.p.Kind)
		if e.p.A != nil && e.p.B != nil {
			fmt.Fprintf(&pb, " (similarity %.2f)", e.p.Similarity)
		}
		pb.WriteString("\n")
		renderUnitSide(&pb, "A", docA, e.p.A, sideMax)
		renderUnitSide(&pb, "B", docB, e.p.B, sideMax)
		a.appendOrphanCrossCheck(ctx, &pb, e.p, docA, dbA, docB, dbB)
		body.WriteString(pb.String())
		runes += len([]rune(pb.String()))
		last = i
	}
	fmt.Fprintf(&sb, "Showing pairs %d-%d of %d%s.\n", start+1, last+1, len(sel), filterNote(filter))
	sb.WriteString(body.String())
	if last+1 < len(sel) {
		fmt.Fprintf(&sb, "\nAfter analysing these pairs, call align_docs again with view=\"pairs\"%s and cursor=%d (%d pairs remain).",
			filterArgNote(filter), sel[last+1].n, len(sel)-last-1)
	} else {
		sb.WriteString("\nThis is the last matching pair — write your analysis now.")
	}
	return sb.String()
}

// orphanSemanticFloor is the cosine floor for the semantic fallback in
// appendOrphanCrossCheck. Set conservatively high and still not fully
// trustworthy: live testing against the EEA regulation corpus caught this
// layer flagging a false positive at 0.86 ("Article 4 Roles and
// responsibilities" in an MoU-template annex vs. the main body's "Article 1.5
// The legal framework" — substantively unrelated, but both dense with shared
// administrative vocabulary: "Beneficiary State", "Donor States", "Financial
// Mechanism"). This is the same formulaic-boilerplate-inflates-cosine failure
// mode that motivated title-overlap-over-cosine in the alignment pool itself
// (see AlignUnits) — it applies here too, and a threshold alone cannot fully
// fix it. 0.9 is a conservative guess pending a real calibration pass (an
// eval over known true/false match pairs, as in docs/rag_research); until
// then this layer's output should be read as "worth a second look", not
// proof — the wording in appendOrphanCrossCheck says so explicitly.
const orphanSemanticFloor = 0.9

// appendOrphanCrossCheck runs two automatic, deterministic checks for an
// only_a/only_b pair's heading against the OTHER document, and appends the
// result inline — a safety net made mandatory rather than optional: a model
// reliably reads what the tool hands it, but did not reliably remember to
// call a separate verification tool before asserting something is new/removed
// (confirmed by live testing — the same false "removed" claim recurred even
// with an explicit skill instruction and an available tool). Only fires for
// only_a/only_b pairs; changed/matched pairs already show both sides.
//
// Layer 1 (literal): does a title-word phrase appear verbatim elsewhere in the
// other document? Catches a move whose wording survived even though alignment
// failed to connect it (e.g. a coincidental renumbering stole the true slot).
//
// Layer 2 (semantic): if layer 1 finds nothing, does any unit's pooled
// embedding in the other document closely resemble this orphan's? Catches a
// move where BOTH the heading and the body were reworded enough that no
// shared vocabulary survives — confirmed necessary live: "External
// monitoring" (old) moved almost verbatim to "Monitoring" under a different
// chapter, which happened to share the word "monitoring" and so was already
// caught by ordinary title-overlap matching; a more thoroughly retitled move
// would share nothing lexical and needs this semantic layer instead.
func (a *Agent) appendOrphanCrossCheck(ctx context.Context, sb *strings.Builder, p docalign.UnitPair, docA *docalign.Doc, dbA *sql.DB, docB *docalign.Doc, dbB *sql.DB) {
	var orphan *docalign.Unit
	var otherDB *sql.DB
	var otherDoc *docalign.Doc
	switch {
	case p.Kind == docalign.OnlyA && p.A != nil:
		orphan, otherDB, otherDoc = p.A, dbB, docB
	case p.Kind == docalign.OnlyB && p.B != nil:
		orphan, otherDB, otherDoc = p.B, dbA, docA
	default:
		return
	}
	terms := docalign.TitleWords(orphan.Heading)
	if len(terms) > 0 {
		phrase := strings.Join(terms, " ")
		if hits, err := a.Docs.FindText(ctx, otherDB, otherDoc.ID, phrase, 2); err == nil && len(hits) > 0 {
			sb.WriteString("[cross-check WARNING: this heading looked like it had no counterpart, but the text ")
			fmt.Fprintf(sb, "%q DOES appear in %q — do NOT report this as new/removed. Read the real counterpart before writing any finding:\n", phrase, otherDoc.Title)
			for _, h := range hits {
				cite := fmt.Sprintf("d.%s.%d", scopeLetter(otherDoc.Scope), h.ChunkID)
				if h.Page > 0 {
					cite += fmt.Sprintf(" p.%d", h.Page)
				}
				fmt.Fprintf(sb, "  [%s] …%s…\n", cite, h.Snippet)
			}
			return
		}
	}
	// Layer 1 found nothing (or the heading had no usable title words at all):
	// fall back to a semantic sweep over the other document's units.
	if best, sim := docalign.BestSemanticMatch(*orphan, docalign.Units(otherDoc), orphanSemanticFloor); best != nil {
		sb.WriteString("[cross-check WARNING: this heading looked like it had no counterpart and no matching text was found, but a section with very similar CONTENT exists under a different heading — ")
		fmt.Fprintf(sb, "%q (similarity %.2f). Read it before reporting this as new/removed; it may be the same provision reworded:\n", best.Heading, sim)
		if len(best.Secs) > 0 {
			cite := fmt.Sprintf("d.%s.%d", scopeLetter(otherDoc.Scope), best.Secs[0].ChunkID)
			if best.Secs[0].Page > 0 {
				cite += fmt.Sprintf(" p.%d", best.Secs[0].Page)
			}
			fmt.Fprintf(sb, "  [%s]\n", cite)
		}
		return
	}
	fmt.Fprintf(sb, "[cross-check: not found — no matching text or similar content anywhere in %q — this appears to be a genuine addition/removal]\n", otherDoc.Title)
}

func filterNote(filter string) string {
	if filter == "" {
		return ""
	}
	return fmt.Sprintf(" matching %q", filter)
}

func filterArgNote(filter string) string {
	if filter == "" {
		return ""
	}
	return fmt.Sprintf(" and filter=%q", filter)
}

// unitPairMatches reports whether a pair's heading or text contains any of the
// comma-separated OR terms in low (already lowercased).
func unitPairMatches(p docalign.UnitPair, low string) bool {
	terms := strings.Split(low, ",")
	for _, u := range []*docalign.Unit{p.A, p.B} {
		if u == nil {
			continue
		}
		heading := strings.ToLower(u.Heading)
		text := strings.ToLower(u.Text())
		for _, t := range terms {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if strings.Contains(heading, t) || strings.Contains(text, t) {
				return true
			}
		}
	}
	return false
}

// parsePairSpec parses a pairs= argument like "9,12,16-19,44" into a sorted,
// deduplicated, in-range list of 1-based pair numbers. A non-empty second
// return value is a tool-facing error.
func parsePairSpec(spec string, maxPair, maxSelectable int) ([]int, string) {
	seen := map[int]bool{}
	var out []int
	add := func(n int) string {
		if n < 1 || n > maxPair {
			return fmt.Sprintf("pair #%d is out of range (1-%d)", n, maxPair)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
		return ""
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok && lo != "" && hi != "" {
			a, erra := strconv.Atoi(strings.TrimSpace(lo))
			b, errb := strconv.Atoi(strings.TrimSpace(hi))
			if erra != nil || errb != nil || a > b {
				return nil, fmt.Sprintf("bad range %q in pairs", part)
			}
			if b-a+1 > maxSelectable {
				return nil, fmt.Sprintf("range %q selects too many pairs (max %d per call)", part, maxSelectable)
			}
			for n := a; n <= b; n++ {
				if errMsg := add(n); errMsg != "" {
					return nil, errMsg
				}
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Sprintf("bad pair number %q in pairs", part)
		}
		if errMsg := add(n); errMsg != "" {
			return nil, errMsg
		}
	}
	if len(out) == 0 {
		return nil, "pairs did not name any valid pair number"
	}
	if len(out) > maxSelectable {
		return nil, fmt.Sprintf("pairs names %d pair numbers, max %d per call — split across a couple of calls", len(out), maxSelectable)
	}
	sort.Ints(out)
	return out, ""
}

// renderUnitPairsExplicit renders exactly the requested pair numbers, batched
// by the explicit-selection budget (higher per-call pair count: the model
// already curated the list from the summary, so it should cost a few calls,
// not one per pair).
func (a *Agent) renderUnitPairsExplicit(ctx context.Context, docA *docalign.Doc, dbA *sql.DB, docB *docalign.Doc, dbB *sql.DB, res *docalign.UnitResult, nums []int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Selected %d pair(s): %s.\n", len(nums), joinInts(nums))
	sideMax := alignSideMaxRunes
	if len(nums) == 1 {
		sideMax = alignBatchMaxRunes * 4 / 5
	}
	runes := 0
	last := 0
	var body strings.Builder
	for i, n := range nums {
		if i >= alignExplicitMaxPairs || (i > 0 && runes >= alignBatchMaxRunes) {
			break
		}
		p := res.Pairs[n-1]
		var pb strings.Builder
		fmt.Fprintf(&pb, "\n### Pair #%d — %s", n, p.Kind)
		if p.A != nil && p.B != nil {
			fmt.Fprintf(&pb, " (similarity %.2f)", p.Similarity)
		}
		pb.WriteString("\n")
		renderUnitSide(&pb, "A", docA, p.A, sideMax)
		renderUnitSide(&pb, "B", docB, p.B, sideMax)
		a.appendOrphanCrossCheck(ctx, &pb, p, docA, dbA, docB, dbB)
		body.WriteString(pb.String())
		runes += len([]rune(pb.String()))
		last = i
	}
	fmt.Fprintf(&sb, "Showing %d of %d requested.\n", last+1, len(nums))
	sb.WriteString(body.String())
	if last+1 < len(nums) {
		fmt.Fprintf(&sb, "\nAfter analysing these, call align_docs again with view=\"pairs\" and pairs=\"%s\" for the rest (%d remain).",
			joinInts(nums[last+1:]), len(nums)-last-1)
	} else {
		sb.WriteString("\nAll requested pairs shown — after analysing them, write your overall analysis.")
	}
	return sb.String()
}

func joinInts(nums []int) string {
	strs := make([]string, len(nums))
	for i, n := range nums {
		strs[i] = strconv.Itoa(n)
	}
	return strings.Join(strs, ",")
}

func unitHeading(u *docalign.Unit) string {
	if u == nil {
		return "?"
	}
	if u.Heading == "" {
		return "(front matter)"
	}
	return u.Heading
}

// renderUnitSide prints one side of a unit pair: heading, page, and the text
// with each chunk's citation id inlined before its content — so a claim can be
// cited with the exact chunk that contains it, not a guess within a range.
func renderUnitSide(sb *strings.Builder, label string, d *docalign.Doc, u *docalign.Unit, sideMax int) {
	if u == nil {
		fmt.Fprintf(sb, "%s: (no counterpart in %q)\n", label, d.Title)
		return
	}
	page := ""
	if u.Secs[0].Page > 0 {
		page = fmt.Sprintf(" (p.%d)", u.Secs[0].Page)
	}
	fmt.Fprintf(sb, "%s %s%s — cite the [d.x.N] id directly before the text you quote:\n", label, unitHeading(u), page)
	runes := 0
	for i, s := range u.Secs {
		part := fmt.Sprintf("[d.%s.%d] %s\n", scopeLetter(d.Scope), s.ChunkID, strings.TrimSpace(s.Text))
		n := len([]rune(part))
		if runes+n > sideMax {
			fmt.Fprintf(sb, "[…CLIPPED: only %d of %d sections shown — the rest of this side exists but is not displayed; do not claim anything is absent from it]\n", i, len(u.Secs))
			return
		}
		sb.WriteString(part)
		runes += n
	}
}

// --- chunk-level fallback rendering (documents without heading structure) ---

// renderAlignment formats a chunk-level batch starting at the 1-based cursor.
func renderAlignment(docA, docB *docalign.Doc, res *docalign.Result, mode string, minSim float64, cursor int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Aligned %q (%s, %d sections) vs %q (%s, %d sections), mode=%s, min_similarity=%.2f.\n",
		docA.Title, docRef(docA), len(docA.Sections), docB.Title, docRef(docB), len(docB.Sections), mode, minSim)
	if mode == "versions" {
		fmt.Fprintf(&sb, "%d identical sections skipped. ", res.Identical)
	}
	c := res.Counts()
	if len(res.Pairs) == 0 {
		if mode == "versions" {
			sb.WriteString("No differences found: the documents are identical section for section.")
		} else {
			sb.WriteString("No sections were paired or left over — nothing to analyse (try a lower min_similarity).")
		}
		return sb.String()
	}
	fmt.Fprintf(&sb, "%d pairs to analyse: %d %s, %d only_a (present only in A), %d only_b (present only in B).\n",
		len(res.Pairs), c[pairedKind(mode)], pairedKind(mode), c[docalign.OnlyA], c[docalign.OnlyB])

	if cursor > len(res.Pairs) {
		fmt.Fprintf(&sb, "Cursor %d is past the last pair (%d): all batches have been retrieved. Write your overall analysis now.", cursor, len(res.Pairs))
		return sb.String()
	}

	runes := 0
	last := cursor - 1 // index of last rendered pair (0-based)
	var body strings.Builder
	for i := cursor - 1; i < len(res.Pairs); i++ {
		if i >= cursor-1+alignBatchMaxPairs || (i > cursor-1 && runes >= alignBatchMaxRunes) {
			break
		}
		p := res.Pairs[i]
		var pb strings.Builder
		fmt.Fprintf(&pb, "\n### Pair %d of %d — %s", i+1, len(res.Pairs), p.Kind)
		if len(p.A) > 0 && len(p.B) > 0 {
			fmt.Fprintf(&pb, " (similarity %.2f)", p.Similarity)
		}
		pb.WriteString("\n")
		renderSide(&pb, "A", docA, p.A)
		renderSide(&pb, "B", docB, p.B)
		body.WriteString(pb.String())
		runes += len([]rune(pb.String()))
		last = i
	}
	fmt.Fprintf(&sb, "Showing pairs %d-%d.\n", cursor, last+1)
	sb.WriteString(body.String())
	if last+1 < len(res.Pairs) {
		fmt.Fprintf(&sb, "\nAfter analysing these pairs, call align_docs again with cursor=%d (%d pairs remain).", last+2, len(res.Pairs)-last-1)
	} else {
		sb.WriteString("\nThis is the last batch — after analysing these pairs, write your overall analysis.")
	}
	return sb.String()
}

func pairedKind(mode string) docalign.Kind {
	if mode == "versions" {
		return docalign.Changed
	}
	return docalign.Matched
}

// renderSide prints one side of a chunk pair: its chunk citations and (clipped)
// text, or a note that the side has no counterpart.
func renderSide(sb *strings.Builder, label string, d *docalign.Doc, secs []docalign.Section) {
	if len(secs) == 0 {
		fmt.Fprintf(sb, "%s: (no counterpart in %q)\n", label, d.Title)
		return
	}
	var cites []string
	var texts []string
	for _, s := range secs {
		cite := fmt.Sprintf("d.%s.%d", scopeLetter(d.Scope), s.ChunkID)
		if s.Page > 0 {
			cite += fmt.Sprintf(" p.%d", s.Page)
		}
		cites = append(cites, cite)
		texts = append(texts, strings.TrimSpace(s.Text))
	}
	text := strings.Join(texts, "\n")
	if r := []rune(text); len(r) > alignSideMaxRunes {
		text = string(r[:alignSideMaxRunes]) + " […clipped]"
	}
	fmt.Fprintf(sb, "%s [%s]:\n%s\n", label, strings.Join(cites, ", "), text)
}

// docRef renders a document's scoped id (e.g. "u.12").
func docRef(d *docalign.Doc) string {
	return scopeLetter(d.Scope) + "." + strconv.FormatInt(d.ID, 10)
}

func scopeLetter(scope string) string {
	// Qualified project scope ("project:<id>", all-projects search) → p<id>.
	if id, ok := strings.CutPrefix(scope, documents.ScopeProject+":"); ok {
		return "p" + id
	}
	switch scope {
	case documents.ScopePersonal:
		return "u"
	case documents.ScopeProject:
		return "p"
	default:
		return "s"
	}
}

// closeForeignDBs releases any cross-project database handles the turn opened
// while resolving p<id>.N references.
func (rc *runContext) closeForeignDBs() {
	for _, db := range rc.foreignProjDBs {
		_ = db.Close()
	}
	rc.foreignProjDBs = nil
}

// projectDBFor returns an open handle to another project's database, lazily
// opened read-only and cached on the runContext for the rest of the turn.
// Membership is enforced. A non-empty second return value is a tool-facing
// error.
func (a *Agent) projectDBFor(ctx context.Context, rc *runContext, projectID int64) (*sql.DB, string) {
	if projectID == rc.projectID && rc.projectDB != nil {
		return rc.projectDB, ""
	}
	if db, ok := rc.foreignProjDBs[projectID]; ok {
		return db, ""
	}
	if a.Projects == nil || a.Storage == nil {
		return nil, "error: cross-project references are not available on this server"
	}
	member := false
	if projs, err := a.Projects.List(ctx, rc.userID); err == nil {
		for _, p := range projs {
			if p.ID == projectID {
				member = true
				break
			}
		}
	}
	if !member {
		return nil, fmt.Sprintf("error: project %d is not among the user's projects", projectID)
	}
	db, err := a.Storage.OpenProjectReadOnly(ctx, projectID)
	if err != nil {
		return nil, fmt.Sprintf("error: can't open project %d: %v", projectID, err)
	}
	if rc.foreignProjDBs == nil {
		rc.foreignProjDBs = map[int64]*sql.DB{}
	}
	rc.foreignProjDBs[projectID] = db
	return db, ""
}

// scopeForToken maps a reference's scope token (u, s, p, or p<id>) to its
// scope label and database for this session. p<id> may reach another project
// the user is a member of. A non-empty third return value is a tool-facing
// error.
func (a *Agent) scopeForToken(ctx context.Context, rc *runContext, token string) (string, *sql.DB, string) {
	switch token {
	case "u":
		return documents.ScopePersonal, rc.userDB, ""
	case "s":
		return documents.ScopeShared, a.Docs.SharedDB(), ""
	case "p":
		if rc.projectDB == nil {
			return "", nil, "error: p.N refers to the current project, but this is not a project session — use the qualified form p<project>.N"
		}
		return documents.ScopeProject, rc.projectDB, ""
	}
	if pid, err := strconv.ParseInt(strings.TrimPrefix(token, "p"), 10, 64); strings.HasPrefix(token, "p") && err == nil && pid > 0 {
		if pid == rc.projectID && rc.projectDB != nil {
			return documents.ScopeProject, rc.projectDB, ""
		}
		db, errMsg := a.projectDBFor(ctx, rc, pid)
		if errMsg != "" {
			return "", nil, errMsg
		}
		return documents.ProjectScope(pid), db, ""
	}
	return "", nil, fmt.Sprintf("error: unknown document scope %q (use u.N, s.N, p.N or p<project>.N)", token)
}

// loadDocRef resolves a model-supplied document reference ("u.12", "s.3",
// "p.7", "p3.7", or a bare id) against the scopes available to this session
// (p<id> reaches other projects the user is a member of) and loads the
// document. A non-empty second return value is a tool-facing error.
func (a *Agent) loadDocRef(ctx context.Context, rc *runContext, ref string) (*docalign.Doc, string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, "error: doc_a and doc_b are required (scoped document ids like u.12, s.3 or p.7)"
	}
	scopes := a.Docs.ScopesFor(rc.userDB, rc.projectDB)
	// Scoped form: <token>.<id> (tolerate the chunk-id style "d.u.N" prefix).
	if parts := strings.Split(strings.TrimPrefix(ref, "d."), "."); len(parts) == 2 {
		scope, db, errMsg := a.scopeForToken(ctx, rc, parts[0])
		if errMsg != "" {
			return nil, errMsg
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Sprintf("error: bad document id in %q", ref)
		}
		d, err := docalign.LoadDoc(ctx, db, scope, id)
		if err != nil {
			return nil, fmt.Sprintf("error: document %s not found", ref)
		}
		return d, ""
	}
	// Bare id: accept it only when exactly one available corpus has it.
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil || id <= 0 {
		return nil, fmt.Sprintf("error: %q is not a document reference (use u.N, s.N, p.N or a bare id)", ref)
	}
	var found *docalign.Doc
	var foundIn []string
	for _, sc := range scopes {
		if sc.DB == nil {
			continue
		}
		if d, err := docalign.LoadDoc(ctx, sc.DB, sc.Scope, id); err == nil {
			found = d
			foundIn = append(foundIn, scopeLetter(sc.Scope)+"."+ref)
		}
	}
	switch len(foundIn) {
	case 1:
		return found, ""
	case 0:
		return nil, fmt.Sprintf("error: no document with id %s in any available corpus", ref)
	default:
		return nil, fmt.Sprintf("error: document id %s is ambiguous (%s) — use the scoped form", ref, strings.Join(foundIn, ", "))
	}
}

// quoteRE finds a long double-quoted claim — long enough to read as an actual
// verbatim excerpt of document text, not a scare-quoted word or short label
// (those are exempt from requiring a citation).
var quoteRE = regexp.MustCompile(`"([^"\n]{15,400})"`)

// citeAfterRE matches a citation within a tight window right after a quote's
// closing mark, so it can't accidentally pair with an unrelated citation
// belonging to a different claim further down. The scope token may be a
// qualified project (p<id>, from an all-projects search).
var citeAfterRE = regexp.MustCompile(`^[^\[\n]{0,60}\[d\.(u|s|p\d*)\.(\d+)\]`)

// VerifyCitedQuotes enforces two things about every long quoted claim in
// content: it must be cited, and the citation must be to the chunk that
// actually contains it (a literal, whitespace/case-insensitive substring
// match). Both are confirmed live failure modes: a model writing several
// similar clauses in one dense sentence sometimes drops the citation on one
// of them entirely, or attaches a real quote to an adjacent, textually
// similar clause's id — a synthesis-time mix-up between two real findings,
// not a fabrication of either one. Returns a description per problem found
// (nil if everything checks out); a citation to a chunk this session can't
// resolve is skipped rather than flagged, so an unrelated ref quirk doesn't
// block a save.
func (a *Agent) VerifyCitedQuotes(ctx context.Context, rc *runContext, content string) []string {
	var problems []string
	for _, loc := range quoteRE.FindAllStringSubmatchIndex(content, -1) {
		quote := content[loc[2]:loc[3]]
		rest := content[loc[1]:]
		m := citeAfterRE.FindStringSubmatch(rest)
		if m == nil {
			problems = append(problems, fmt.Sprintf(
				"the quoted claim %q has no [d.x.N] citation right after it — every quoted excerpt must be immediately followed by the citation of the chunk it came from; add one, or remove the quotation marks if this is a paraphrase rather than a verbatim quote",
				quote))
			continue
		}
		letter, idStr := m[1], m[2]
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			continue
		}
		_, db, errMsg := a.scopeForToken(ctx, rc, letter)
		if errMsg != "" || db == nil {
			continue
		}
		chunkText, err := a.Docs.ChunkContent(ctx, db, id)
		if err != nil || strings.TrimSpace(chunkText) == "" {
			continue
		}
		if !normalizedContains(chunkText, quote) {
			preview := chunkText
			if r := []rune(preview); len(r) > 200 {
				preview = string(r[:200]) + "…"
			}
			problems = append(problems, fmt.Sprintf("quote %q is cited as [d.%s.%d], but that chunk's actual text is: %q — find the chunk that really contains this quote (search_docs or re-check the align_docs pair) and fix the citation",
				quote, letter, id, preview))
		}
	}
	return problems
}

// normalizedContains reports whether needle appears in haystack, ignoring case
// and collapsing whitespace runs (PDF-extracted text often reflows spacing).
func normalizedContains(haystack, needle string) bool {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	return strings.Contains(norm(haystack), norm(needle))
}

// docDBForScope resolves the corpus DB backing a given scope label for this
// session — the same mapping loadDocRef used to find the document originally.
// Qualified project scopes ("project:<id>") resolve through the turn's
// cross-project cache.
func (a *Agent) docDBForScope(ctx context.Context, rc *runContext, scope string) (*sql.DB, string) {
	if idStr, ok := strings.CutPrefix(scope, documents.ScopeProject+":"); ok {
		pid, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || pid <= 0 {
			return nil, fmt.Sprintf("error: bad project scope %q", scope)
		}
		return a.projectDBFor(ctx, rc, pid)
	}
	for _, sc := range a.Docs.ScopesFor(rc.userDB, rc.projectDB) {
		if sc.Scope == scope {
			return sc.DB, ""
		}
	}
	return nil, fmt.Sprintf("error: no corpus available for scope %q", scope)
}

// argFloat reads a float tool argument (JSON numbers decode as float64).
func argFloat(args map[string]any, key string, def float64) float64 {
	if v, ok := args[key].(float64); ok {
		return v
	}
	return def
}

// parseDocRefs groups scoped document refs ("u.2", "p.3") by scope label for a
// documents search filter. Empty input yields a nil map (no filter). A non-empty
// errMsg is a tool-facing error.
func parseDocRefs(refs []string) (map[string][]int64, string) {
	if len(refs) == 0 {
		return nil, ""
	}
	out := map[string][]int64{}
	for _, ref := range refs {
		parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(ref, "d.")), ".")
		if len(parts) != 2 {
			return nil, fmt.Sprintf("error: bad document ref %q in docs (want u.N, s.N or p.N — see list_documents)", ref)
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Sprintf("error: bad document id in %q", ref)
		}
		var scope string
		switch parts[0] {
		case "u":
			scope = documents.ScopePersonal
		case "s":
			scope = documents.ScopeShared
		case "p":
			scope = documents.ScopeProject
		default:
			// p<id>: a specific project's corpus (all-projects search).
			pid, perr := strconv.ParseInt(strings.TrimPrefix(parts[0], "p"), 10, 64)
			if !strings.HasPrefix(parts[0], "p") || perr != nil || pid <= 0 {
				return nil, fmt.Sprintf("error: unknown scope %q in %q (want u.N, s.N, p.N or p<project>.N)", parts[0], ref)
			}
			scope = documents.ProjectScope(pid)
		}
		out[scope] = append(out[scope], id)
	}
	return out, ""
}
