package docalign

import (
	"regexp"
	"sort"
	"strings"
)

// Chunk-level alignment (docalign.go) drowns long structured documents: two
// separately-converted PDFs share almost no byte-identical chunks, so a pair of
// regulations yields hundreds of low-level pairs. This file aligns at the
// natural granularity of such documents instead: units — article/section-sized
// groups of consecutive chunks under one heading — matched primarily by their
// heading anchor ("Article 6.4"), which survives wording rewrites, with
// embedding matching for whatever carries no usable anchor.

// Unit is one alignment unit: a heading plus its consecutive sections.
type Unit struct {
	Heading string // display heading, "" for front matter before the first heading
	Key     string // normalized anchor, e.g. "article 6.4"; "" when not numbered
	Secs    []Section
}

// Text is the unit's full concatenated text.
func (u *Unit) Text() string {
	parts := make([]string, len(u.Secs))
	for i, s := range u.Secs {
		parts[i] = s.Text
	}
	return strings.Join(parts, "\n")
}

// vec pools the unit's section embeddings (mean); nil if none are embedded.
func (u *Unit) vec() []float32 {
	var sum []float32
	n := 0
	for _, s := range u.Secs {
		if len(s.Vec) == 0 {
			continue
		}
		if sum == nil {
			sum = make([]float32, len(s.Vec))
		}
		if len(s.Vec) != len(sum) {
			continue
		}
		for i, v := range s.Vec {
			sum[i] += v
		}
		n++
	}
	if n == 0 {
		return nil
	}
	for i := range sum {
		sum[i] /= float32(n)
	}
	return sum
}

// UnitPair is one aligned pair of units (either side may be nil for OnlyA/OnlyB).
type UnitPair struct {
	Kind       Kind
	A, B       *Unit
	Similarity float64
}

// UnitResult is a unit-level alignment of two documents.
type UnitResult struct {
	Mode      string
	Identical int // matched units whose text is equal (not in Pairs)
	UnitsA    int
	UnitsB    int
	Pairs     []UnitPair
}

// Counts tallies pairs by kind.
func (r *UnitResult) Counts() map[Kind]int {
	out := map[Kind]int{}
	for _, p := range r.Pairs {
		out[p.Kind]++
	}
	return out
}

// headingRE finds a heading line inside a chunk: a Markdown heading (Docling
// ingests) or a bare numbered structural line (plain-text extractions — strict,
// number-only, so cross-references inside sentences don't split units).
var headingRE = regexp.MustCompile(`(?mi)^(?:#{1,4} +(.+)|((?:article|chapter|annex) +\d+(?:\. ?\d+)*[a-z]?) *)$`)

// keyRE extracts the normalized anchor from a heading ("Article 1. 3 Principles"
// → "article 1.3"; converters sometimes insert spaces inside the number).
var keyRE = regexp.MustCompile(`(?i)\b(article|chapter|annex)\s+(\d+(?:\.\s*\d+)*[a-z]?)`)

// truncatedHeadingRE recognizes a heading whose article number was split by
// the converter across the line break ("## Article 1." … next line "12 …").
var truncatedHeadingRE = regexp.MustCompile(`(?i)(article|chapter|annex)\s+\d+\.$`)

// headingOf returns the first heading line in text and its normalized key.
func headingOf(text string) (heading, key string) {
	loc := headingRE.FindStringSubmatchIndex(text)
	if loc == nil {
		return "", ""
	}
	m := headingRE.FindStringSubmatch(text)
	heading = m[1]
	if heading == "" {
		heading = m[2]
	}
	heading = strings.Join(strings.Fields(heading), " ")
	// Converters sometimes break the number across the heading line: repair
	// "Article 1." from the text following the heading within this chunk (the
	// cross-chunk case is repaired in Units, which sees the next section).
	heading = continueHeading(heading, text[loc[1]:])
	return heading, keyOf(heading)
}

// keyOf extracts the normalized anchor from a heading, or "".
func keyOf(heading string) string {
	km := keyRE.FindStringSubmatch(heading)
	if km == nil {
		return ""
	}
	num := strings.ReplaceAll(strings.ReplaceAll(km[2], " ", ""), "..", ".")
	return strings.ToLower(km[1]) + " " + strings.TrimRight(num, ".")
}

// continueHeading appends a number continuation from following text to a
// truncated heading ("Article 1." + "12 Completion of …"). Returns the heading
// unchanged when next doesn't continue it.
func continueHeading(heading, next string) string {
	if !truncatedHeadingRE.MatchString(heading) {
		return heading
	}
	line, _, _ := strings.Cut(strings.TrimSpace(next), "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 || strings.Trim(fields[0], "0123456789") != "" {
		return heading
	}
	if len(fields) > 12 {
		fields = fields[:12]
	}
	return heading + " " + strings.Join(fields, " ")
}

// titleStopwords are words too common in section titles to signal
// correspondence on their own.
var titleStopwords = map[string]bool{
	"the": true, "of": true, "and": true, "for": true, "in": true, "to": true,
	"a": true, "an": true, "on": true, "by": true, "with": true, "at": true,
}

// titleOverlap reports whether two units' heading titles (the words after the
// numbered key, minus stopwords) share at least half of the shorter title's
// words. Number-only headings have no title to compare — that is not overlap.
func titleOverlap(a, b Unit) bool {
	wa := titleWords(a.Heading)
	wb := titleWords(b.Heading)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	shorter, longer := wa, wb
	if len(wb) < len(wa) {
		shorter, longer = wb, wa
	}
	in := map[string]bool{}
	for _, w := range longer {
		in[w] = true
	}
	hits := 0
	for _, w := range shorter {
		if in[w] {
			hits++
		}
	}
	// Strictly more than half: a single shared filler word ("thing", "matter")
	// between two otherwise-unrelated two-word titles must not count as
	// overlap, since this signal can override cosine similarity in the
	// embedding pool (see AlignUnits) and force an incorrect cross-number match.
	return hits*2 > len(shorter)
}

// TitleWords is titleWords, exported for callers outside this package that
// need a heading's descriptive terms (e.g. to phrase an authoritative
// full-text presence check against the other document).
func TitleWords(heading string) []string { return titleWords(heading) }

// titleWords lowercases a heading, drops its numbered key portion and
// stopwords, keeping the descriptive words ("Article 6.4 Grant rates and size"
// → [grant rates size]).
func titleWords(heading string) []string {
	fields := strings.Fields(strings.ToLower(heading))
	// Skip the leading label ("article"/"chapter"/"annex") and its number,
	// which may be spaced ("article 6. 4").
	i := 0
	for i < len(fields) {
		f := strings.Trim(fields[i], ".")
		if i == 0 && (f == "article" || f == "chapter" || f == "annex") {
			i++
			continue
		}
		if f == "" || strings.Trim(f, "0123456789.") == "" {
			i++
			continue
		}
		break
	}
	var out []string
	for _, f := range fields[i:] {
		if !titleStopwords[f] {
			out = append(out, f)
		}
	}
	return out
}

// Units groups a document's sections into heading units: a section that
// contains a heading starts a new unit; sections before the first heading form
// the front-matter unit. A single section/chunk can contain SEVERAL headings —
// short back-to-back articles/chapters often land in one physical chunk after
// chunking — so every heading occurrence in a section is found and split into
// its own fragment, each still citing the section's chunk id (citations are
// chunk-granular; splitting further than that would break "[d.x.N]" lookups).
func Units(d *Doc) []Unit {
	var out []Unit
	cur := Unit{}
	flush := func() {
		if len(cur.Secs) > 0 {
			out = append(out, cur)
		}
	}
	for i, s := range d.Sections {
		locs := headingRE.FindAllStringIndex(s.Text, -1)
		if len(locs) == 0 {
			cur.Secs = append(cur.Secs, s)
			continue
		}
		if locs[0][0] > 0 {
			pre := s
			pre.Text = s.Text[:locs[0][0]]
			if strings.TrimSpace(pre.Text) != "" {
				cur.Secs = append(cur.Secs, pre)
			}
		}
		for hi, loc := range locs {
			end := len(s.Text)
			if hi+1 < len(locs) {
				end = locs[hi+1][0]
			}
			frag := s
			frag.Text = s.Text[loc[0]:end]
			h, k := headingOf(frag.Text)
			// A heading cut off at the chunk boundary ("## Article 1." ending
			// the chunk) continues in the next section's opening text — only
			// possible for the LAST heading of the LAST section (an earlier
			// heading's fragment ends at the next heading in this same chunk,
			// which is never a valid continuation of a truncated number).
			if h != "" && hi == len(locs)-1 && i+1 < len(d.Sections) {
				if repaired := continueHeading(h, d.Sections[i+1].Text); repaired != h {
					h = repaired
					k = keyOf(h)
				}
			}
			flush()
			cur = Unit{Heading: h, Key: k, Secs: []Section{frag}}
		}
	}
	flush()
	return out
}

// AlignUnits aligns two documents at unit granularity. Units whose keys are
// unique on both sides anchor by key (equal text → Identical, else a pair —
// Changed in versions mode, Matched in topical mode). The rest match greedily
// by embedding similarity above minSim; leftovers become OnlyA/OnlyB. Pairs
// follow document A's order, with B-only units appended in B's order.
func AlignUnits(a, b *Doc, mode string, minSim float64) *UnitResult {
	ua, ub := Units(a), Units(b)
	res := &UnitResult{Mode: mode, UnitsA: len(ua), UnitsB: len(ub)}
	pairedKind := Matched
	if mode == "versions" {
		pairedKind = Changed
	}

	uniqueKeys := func(us []Unit) map[string]int {
		count := map[string]int{}
		for _, u := range us {
			if u.Key != "" {
				count[u.Key]++
			}
		}
		idx := map[string]int{}
		for i, u := range us {
			if u.Key != "" && count[u.Key] == 1 {
				idx[u.Key] = i
			}
		}
		return idx
	}
	ka, kb := uniqueKeys(ua), uniqueKeys(ub)

	matchOfA := make([]int, len(ua))
	matchOfB := make([]int, len(ub))
	for i := range matchOfA {
		matchOfA[i] = -1
	}
	for j := range matchOfB {
		matchOfB[j] = -1
	}
	// In versions mode the embedding pool holds renumbered, removed and added
	// articles. Same-domain legal text has a high baseline similarity (~0.6),
	// so pairing at the caller's floor would glue removed articles to random
	// counterparts; a true renumbered article is a near-copy. Raise the floor.
	poolSim := minSim
	if mode == "versions" && poolSim < 0.75 {
		poolSim = 0.75
	}

	// A shared key anchors a pair only when the contents plausibly correspond:
	// articles get renumbered between versions (old 8.8 may be new 8.9), and
	// annex templates reuse the main body's numbering — so a bare number match
	// with neither a similar title nor a very similar body is released to the
	// embedding pool, where the true counterpart can claim it.
	for key, i := range ka {
		j, ok := kb[key]
		if !ok {
			continue
		}
		hasTitle := len(titleWords(ua[i].Heading)) > 0 && len(titleWords(ub[j].Heading)) > 0
		switch {
		case titleOverlap(ua[i], ub[j]):
			matchOfA[i] = j
			matchOfB[j] = i
		case !hasTitle && cosine(ua[i].vec(), ub[j].vec()) >= poolSim:
			// Neither side has a comparable title (a bare numeric heading) —
			// the shared number plus strong body-text similarity is the only
			// signal available, so accept it.
			matchOfA[i] = j
			matchOfB[j] = i
		default:
			// Same number, but real, non-overlapping titles: article
			// renumbering can put two unrelated articles at the same number
			// across versions (e.g. old 12.6 "Reporting on progress" vs new
			// 12.6 "Complaint mechanism", while the true old "Complaint
			// mechanism" is 12.7). Leave both unmatched so the embedding pool
			// — which prefers title matches document-wide, not just within
			// this number — can find each unit's real counterpart instead of
			// this pairing stealing the slot the true match needs.
		}
	}

	// Greedy embedding matching for whatever the anchors left over.
	type cand struct {
		i, j       int
		sim        float64
		titleMatch bool
	}
	var cands []cand
	for i := range ua {
		if matchOfA[i] != -1 {
			continue
		}
		va := ua[i].vec()
		for j := range ub {
			if matchOfB[j] != -1 {
				continue
			}
			// A renumbered article ("old 4.4 Cooperation Committee" -> "new 4.3
			// Cooperation Committee") never reaches this pool via a shared key
			// (the numbers differ), but its title is still the strongest
			// available signal — often stronger than body-text cosine, which
			// can coincidentally favor a same-boilerplate different-topic
			// article in formulaic regulatory text. Title-matched candidates
			// are tried first regardless of embedding similarity.
			tm := titleOverlap(ua[i], ub[j])
			if !tm {
				if va == nil {
					continue
				}
				sim := cosine(va, ub[j].vec())
				if sim < poolSim {
					continue
				}
				cands = append(cands, cand{i, j, sim, false})
				continue
			}
			sim := 0.0
			if va != nil {
				sim = cosine(va, ub[j].vec())
			}
			cands = append(cands, cand{i, j, sim, true})
		}
	}
	sort.Slice(cands, func(x, y int) bool {
		if cands[x].titleMatch != cands[y].titleMatch {
			return cands[x].titleMatch // title matches win over pure embedding matches
		}
		if cands[x].sim != cands[y].sim {
			return cands[x].sim > cands[y].sim
		}
		if cands[x].i != cands[y].i {
			return cands[x].i < cands[y].i
		}
		return cands[x].j < cands[y].j
	})
	for _, c := range cands {
		if matchOfA[c.i] == -1 && matchOfB[c.j] == -1 {
			matchOfA[c.i] = c.j
			matchOfB[c.j] = c.i
		}
	}

	for i := range ua {
		j := matchOfA[i]
		if j == -1 {
			res.Pairs = append(res.Pairs, UnitPair{Kind: OnlyA, A: &ua[i]})
			continue
		}
		if normText(ua[i].Text()) == normText(ub[j].Text()) {
			res.Identical++
			continue
		}
		res.Pairs = append(res.Pairs, UnitPair{
			Kind: pairedKind, A: &ua[i], B: &ub[j],
			Similarity: cosine(ua[i].vec(), ub[j].vec()),
		})
	}
	for j := range ub {
		if matchOfB[j] == -1 {
			res.Pairs = append(res.Pairs, UnitPair{Kind: OnlyB, B: &ub[j]})
		}
	}
	return res
}
