// Package docalign aligns the chunks of two documents so their differences can
// be analysed one small piece at a time. "versions" mode diffs two revisions of
// the same text: exact-match sections anchor the alignment (and are skipped as
// identical), and the regions between anchors are paired by embedding
// similarity into changed/added/removed pairs. "topical" mode compares two
// different texts about the same subject: sections are greedily paired across
// the documents by embedding cosine similarity, and sections with no
// counterpart become "only in A/B" findings. All of this is deterministic —
// no LLM is involved; the agent's align_docs tool walks the result so the
// model only ever analyses one aligned pair at a time.
package docalign

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Section is one document chunk with its stored embedding (nil when the chunk
// was never embedded).
type Section struct {
	ChunkID int64
	Ord     int
	Page    int
	Text    string
	Vec     []float32
}

// Doc is a document's chunk sequence loaded from a corpus DB.
type Doc struct {
	ID       int64
	Title    string
	Scope    string // corpus scope label ("personal"/"shared"/"project")
	Sections []Section
}

// Kind classifies an aligned pair.
type Kind string

const (
	// Changed: versions mode — the two sides occupy the same position but differ.
	Changed Kind = "changed"
	// Matched: topical mode — the two sides cover the same topic.
	Matched Kind = "matched"
	// OnlyA / OnlyB: sections with no counterpart in the other document.
	OnlyA Kind = "only_a"
	OnlyB Kind = "only_b"
)

// Pair is one unit of work for the analysing model: a group of sections from
// each side (either may be empty for OnlyA/OnlyB).
type Pair struct {
	Kind       Kind
	A, B       []Section
	Similarity float64 // cosine of the paired sections (0 when a side is empty)
}

// Result is a full alignment of two documents.
type Result struct {
	Mode      string
	Identical int // versions mode: sections equal on both sides (not in Pairs)
	Pairs     []Pair
}

// Counts tallies pairs by kind.
func (r *Result) Counts() map[Kind]int {
	out := map[Kind]int{}
	for _, p := range r.Pairs {
		out[p.Kind]++
	}
	return out
}

// LoadDoc reads a document's title and chunk sequence (with stored embeddings)
// from a corpus DB.
func LoadDoc(ctx context.Context, db *sql.DB, scope string, docID int64) (*Doc, error) {
	d := &Doc{ID: docID, Scope: scope}
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(title, '') FROM documents WHERE id = ?`, docID).Scan(&d.Title); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, ord, page, content FROM doc_chunks WHERE document_id = ? ORDER BY ord`, docID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s Section
		if err := rows.Scan(&s.ChunkID, &s.Ord, &s.Page, &s.Text); err != nil {
			rows.Close()
			return nil, err
		}
		d.Sections = append(d.Sections, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Vector lookups after the chunk cursor is closed (vec0 point queries).
	for i := range d.Sections {
		var blob []byte
		err := db.QueryRowContext(ctx,
			`SELECT embedding FROM doc_chunks_vec WHERE rowid = ?`, d.Sections[i].ChunkID).Scan(&blob)
		if err == nil {
			d.Sections[i].Vec = decodeVec(blob)
		}
	}
	return d, nil
}

// decodeVec parses a sqlite-vec float32 blob (little-endian).
func decodeVec(blob []byte) []float32 {
	if len(blob) == 0 || len(blob)%4 != 0 {
		return nil
	}
	out := make([]float32, len(blob)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return out
}

// AlignVersions diffs two revisions of the same text. Sections whose normalized
// text matches exactly anchor the alignment and count as Identical; the gaps
// between anchors are aligned monotonically by embedding similarity (pairs
// whose net similarity clears minSim), leaving the rest as OnlyA/OnlyB.
func AlignVersions(a, b *Doc, minSim float64) *Result {
	ak := normKeys(a.Sections)
	bk := normKeys(b.Sections)
	var matches [][2]int
	patience(ak, bk, 0, len(ak), 0, len(bk), &matches)

	res := &Result{Mode: "versions", Identical: len(matches)}
	pa, pb := 0, 0
	// Sentinel closes the final gap.
	matches = append(matches, [2]int{len(ak), len(bk)})
	for _, m := range matches {
		gap := alignGap(a.Sections[pa:m[0]], b.Sections[pb:m[1]], minSim)
		for _, p := range gap {
			// An equal-text pair can surface from the gap aligner when the text
			// repeats (patience only anchors unique sections): still identical.
			if p.Kind == Changed && len(p.A) == 1 && len(p.B) == 1 &&
				normText(p.A[0].Text) == normText(p.B[0].Text) {
				res.Identical++
				continue
			}
			res.Pairs = append(res.Pairs, p)
		}
		pa, pb = m[0]+1, m[1]+1
	}
	return res
}

// AlignTopical pairs sections of two different documents about the same subject
// by greedy best-first embedding similarity (each section matches at most once,
// only above minSim). Unmatched sections become OnlyA/OnlyB pairs. Matched and
// OnlyA pairs are ordered by document A; OnlyB groups follow in B's order.
func AlignTopical(a, b *Doc, minSim float64) (*Result, error) {
	if n := missingVecs(a.Sections); n > 0 {
		return nil, fmt.Errorf("document %q: %d of %d sections have no embedding (re-ingest or reindex it)", a.Title, n, len(a.Sections))
	}
	if n := missingVecs(b.Sections); n > 0 {
		return nil, fmt.Errorf("document %q: %d of %d sections have no embedding (re-ingest or reindex it)", b.Title, n, len(b.Sections))
	}
	type cand struct {
		i, j int
		sim  float64
	}
	cands := make([]cand, 0, len(a.Sections)*len(b.Sections))
	for i := range a.Sections {
		for j := range b.Sections {
			if sim := cosine(a.Sections[i].Vec, b.Sections[j].Vec); sim >= minSim {
				cands = append(cands, cand{i, j, sim})
			}
		}
	}
	sort.Slice(cands, func(x, y int) bool {
		if cands[x].sim != cands[y].sim {
			return cands[x].sim > cands[y].sim
		}
		if cands[x].i != cands[y].i {
			return cands[x].i < cands[y].i
		}
		return cands[x].j < cands[y].j
	})
	matchOfA := make([]int, len(a.Sections))
	matchOfB := make([]int, len(b.Sections))
	for i := range matchOfA {
		matchOfA[i] = -1
	}
	for j := range matchOfB {
		matchOfB[j] = -1
	}
	simOfA := make([]float64, len(a.Sections))
	for _, c := range cands {
		if matchOfA[c.i] == -1 && matchOfB[c.j] == -1 {
			matchOfA[c.i] = c.j
			matchOfB[c.j] = c.i
			simOfA[c.i] = c.sim
		}
	}

	res := &Result{Mode: "topical"}
	// Walk A in order: matched pairs and merged runs of unmatched A sections.
	var runA []Section
	flushA := func() {
		res.Pairs = append(res.Pairs, groupRun(runA, OnlyA)...)
		runA = nil
	}
	for i, s := range a.Sections {
		if matchOfA[i] == -1 {
			runA = append(runA, s)
			continue
		}
		flushA()
		res.Pairs = append(res.Pairs, Pair{
			Kind: Matched, A: []Section{s}, B: []Section{b.Sections[matchOfA[i]]}, Similarity: simOfA[i],
		})
	}
	flushA()
	var runB []Section
	for j, s := range b.Sections {
		if matchOfB[j] == -1 {
			runB = append(runB, s)
			continue
		}
		res.Pairs = append(res.Pairs, groupRun(runB, OnlyB)...)
		runB = nil
	}
	res.Pairs = append(res.Pairs, groupRun(runB, OnlyB)...)
	return res, nil
}

// maxRunGroup caps how many sections a merged OnlyA/OnlyB pair carries, so a
// long unmatched run becomes several evenly-sized work units instead of one
// oversized one.
const maxRunGroup = 3

// groupRun turns a run of same-side sections into pairs of at most maxRunGroup.
func groupRun(run []Section, kind Kind) []Pair {
	var out []Pair
	for start := 0; start < len(run); start += maxRunGroup {
		end := min(start+maxRunGroup, len(run))
		p := Pair{Kind: kind}
		if kind == OnlyA {
			p.A = run[start:end]
		} else {
			p.B = run[start:end]
		}
		out = append(out, p)
	}
	return out
}

// alignGap aligns one between-anchors region of a versions diff. With
// embeddings on both sides (and a bounded matrix), a monotonic global alignment
// pairs sections whose similarity clears minSim; otherwise sections pair
// sequentially. Leftovers become OnlyA/OnlyB groups.
func alignGap(as, bs []Section, minSim float64) []Pair {
	switch {
	case len(as) == 0 && len(bs) == 0:
		return nil
	case len(bs) == 0:
		return groupRun(as, OnlyA)
	case len(as) == 0:
		return groupRun(bs, OnlyB)
	}
	if missingVecs(as) > 0 || missingVecs(bs) > 0 || len(as)*len(bs) > 250_000 {
		return pairSequential(as, bs)
	}
	// Needleman–Wunsch with zero-cost gaps and (sim - minSim) match scores: a
	// pairing below minSim scores negative, so the alignment prefers leaving
	// both sections unpaired.
	n, m := len(as), len(bs)
	score := make([][]float64, n+1)
	move := make([][]byte, n+1) // 'd' diag, 'u' up (gap in B), 'l' left (gap in A)
	for i := 0; i <= n; i++ {
		score[i] = make([]float64, m+1)
		move[i] = make([]byte, m+1)
	}
	for i := 1; i <= n; i++ {
		move[i][0] = 'u'
	}
	for j := 1; j <= m; j++ {
		move[0][j] = 'l'
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			diag := score[i-1][j-1] + cosine(as[i-1].Vec, bs[j-1].Vec) - minSim
			up := score[i-1][j]
			left := score[i][j-1]
			score[i][j], move[i][j] = diag, 'd'
			if up > score[i][j] {
				score[i][j], move[i][j] = up, 'u'
			}
			if left > score[i][j] {
				score[i][j], move[i][j] = left, 'l'
			}
		}
	}
	// Backtrack, then reverse into document order.
	type op struct {
		kind Kind
		a, b int
	}
	var ops []op
	for i, j := n, m; i > 0 || j > 0; {
		switch move[i][j] {
		case 'd':
			ops = append(ops, op{Changed, i - 1, j - 1})
			i--
			j--
		case 'u':
			ops = append(ops, op{OnlyA, i - 1, -1})
			i--
		default:
			ops = append(ops, op{OnlyB, -1, j - 1})
			j--
		}
	}
	var out []Pair
	var runA, runB []Section
	flush := func() {
		out = append(out, groupRun(runA, OnlyA)...)
		out = append(out, groupRun(runB, OnlyB)...)
		runA, runB = nil, nil
	}
	for k := len(ops) - 1; k >= 0; k-- {
		o := ops[k]
		switch o.kind {
		case Changed:
			flush()
			out = append(out, Pair{
				Kind: Changed, A: []Section{as[o.a]}, B: []Section{bs[o.b]},
				Similarity: cosine(as[o.a].Vec, bs[o.b].Vec),
			})
		case OnlyA:
			runA = append(runA, as[o.a])
		default:
			runB = append(runB, bs[o.b])
		}
	}
	flush()
	return out
}

// pairSequential pairs a gap 1:1 in order — the no-embeddings fallback.
func pairSequential(as, bs []Section) []Pair {
	n := min(len(as), len(bs))
	var out []Pair
	for i := range n {
		out = append(out, Pair{
			Kind: Changed, A: []Section{as[i]}, B: []Section{bs[i]},
			Similarity: cosine(as[i].Vec, bs[i].Vec),
		})
	}
	out = append(out, groupRun(as[n:], OnlyA)...)
	out = append(out, groupRun(bs[n:], OnlyB)...)
	return out
}

// patience emits, in document order, the aligned index pairs of sections whose
// normalized text matches exactly: common prefix/suffix first, then
// patience-diff anchors (sections unique in both ranges, longest increasing
// subsequence), recursing between anchors. Small anchor-free ranges fall back
// to an LCS so repeated sections still match; large anchor-free ranges are
// left to the embedding gap aligner.
func patience(ak, bk []string, aLo, aHi, bLo, bHi int, out *[][2]int) {
	for aLo < aHi && bLo < bHi && ak[aLo] == bk[bLo] {
		*out = append(*out, [2]int{aLo, bLo})
		aLo++
		bLo++
	}
	var suffix [][2]int
	for aLo < aHi && bLo < bHi && ak[aHi-1] == bk[bHi-1] {
		suffix = append(suffix, [2]int{aHi - 1, bHi - 1})
		aHi--
		bHi--
	}
	// Suffix matches are emitted after everything inside the narrowed range.
	defer func() {
		for i := len(suffix) - 1; i >= 0; i-- {
			*out = append(*out, suffix[i])
		}
	}()
	if aLo >= aHi || bLo >= bHi {
		return
	}
	type occ struct {
		n, idx int
	}
	ca := map[string]occ{}
	for i := aLo; i < aHi; i++ {
		o := ca[ak[i]]
		o.n++
		o.idx = i
		ca[ak[i]] = o
	}
	cb := map[string]occ{}
	for j := bLo; j < bHi; j++ {
		o := cb[bk[j]]
		o.n++
		o.idx = j
		cb[bk[j]] = o
	}
	var cand [][2]int // unique-in-both matches, in A order
	for i := aLo; i < aHi; i++ {
		if ca[ak[i]].n != 1 {
			continue
		}
		if o, ok := cb[ak[i]]; ok && o.n == 1 {
			cand = append(cand, [2]int{i, o.idx})
		}
	}
	if len(cand) == 0 {
		if (aHi-aLo)*(bHi-bLo) <= 65536 {
			lcs(ak, bk, aLo, aHi, bLo, bHi, out)
		}
		return
	}
	anchors := lisByB(cand)
	prevA, prevB := aLo, bLo
	for _, an := range anchors {
		patience(ak, bk, prevA, an[0], prevB, an[1], out)
		*out = append(*out, an)
		prevA, prevB = an[0]+1, an[1]+1
	}
	patience(ak, bk, prevA, aHi, prevB, bHi, out)
}

// lisByB keeps the longest subsequence of candidates (already in A order) whose
// B indices strictly increase — the classic patience-sorting step.
func lisByB(cand [][2]int) [][2]int {
	tails := []int{}      // tails[k] = index into cand of the smallest B-tail of a k+1-long chain
	prev := make([]int, len(cand))
	for i, c := range cand {
		lo, hi := 0, len(tails)
		for lo < hi {
			mid := (lo + hi) / 2
			if cand[tails[mid]][1] < c[1] {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo > 0 {
			prev[i] = tails[lo-1]
		} else {
			prev[i] = -1
		}
		if lo == len(tails) {
			tails = append(tails, i)
		} else {
			tails[lo] = i
		}
	}
	if len(tails) == 0 {
		return nil
	}
	out := make([][2]int, len(tails))
	for i, k := len(tails)-1, tails[len(tails)-1]; i >= 0; i-- {
		out[i] = cand[k]
		k = prev[k]
	}
	return out
}

// lcs emits the longest common subsequence of two small key ranges, in order.
func lcs(ak, bk []string, aLo, aHi, bLo, bHi int, out *[][2]int) {
	n, m := aHi-aLo, bHi-bLo
	dp := make([][]int, n+1)
	for i := 0; i <= n; i++ {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if ak[aLo+i-1] == bk[bLo+j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	var rev [][2]int
	for i, j := n, m; i > 0 && j > 0; {
		switch {
		case ak[aLo+i-1] == bk[bLo+j-1]:
			rev = append(rev, [2]int{aLo + i - 1, bLo + j - 1})
			i--
			j--
		case dp[i-1][j] >= dp[i][j-1]:
			i--
		default:
			j--
		}
	}
	for i := len(rev) - 1; i >= 0; i-- {
		*out = append(*out, rev[i])
	}
}

func normKeys(secs []Section) []string {
	out := make([]string, len(secs))
	for i, s := range secs {
		out[i] = normText(s.Text)
	}
	return out
}

// normText collapses runs of whitespace so cosmetic reflowing doesn't defeat
// exact matching.
func normText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func missingVecs(secs []Section) int {
	n := 0
	for _, s := range secs {
		if len(s.Vec) == 0 {
			n++
		}
	}
	return n
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
