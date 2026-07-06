package docalign

import (
	"strings"
	"testing"
)

func TestHeadingOf(t *testing.T) {
	cases := []struct {
		text, heading, key string
	}{
		{"intro\n## Article 1. 3 Principles of implementation\nbody", "Article 1. 3 Principles of implementation", "article 1.3"},
		{"## Article 6.4 Grant rates and size of project grants\n1. The…", "Article 6.4 Grant rates and size of project grants", "article 6.4"},
		{"## ANNEXES\ntext", "ANNEXES", ""},
		{"Article 8.13\nProof of conditions", "Article 8.13", "article 8.13"},
		{"see Article 6.5, projects are selected", "", ""}, // cross-reference, not a heading
		{"plain text only", "", ""},
	}
	for _, c := range cases {
		h, k := headingOf(c.text)
		if h != c.heading || k != c.key {
			t.Errorf("headingOf(%q) = (%q, %q), want (%q, %q)", c.text, h, k, c.heading, c.key)
		}
	}
}

func unitDoc(title string, texts ...string) *Doc {
	d := &Doc{Title: title, Scope: "project"}
	for i, txt := range texts {
		d.Sections = append(d.Sections, sec(i, txt, vec(float64(i%5), 1)))
	}
	return d
}

func TestUnitsGrouping(t *testing.T) {
	d := unitDoc("x",
		"front matter",
		"## Article 1.1 Subject\nbody a",
		"continuation of 1.1",
		"## Article 1.2 Objectives\nbody b",
	)
	us := Units(d)
	if len(us) != 3 {
		t.Fatalf("want 3 units (front, 1.1, 1.2), got %d", len(us))
	}
	if us[0].Heading != "" || len(us[0].Secs) != 1 {
		t.Fatalf("front matter unit wrong: %+v", us[0])
	}
	if us[1].Key != "article 1.1" || len(us[1].Secs) != 2 {
		t.Fatalf("unit 1.1 wrong: key=%q secs=%d", us[1].Key, len(us[1].Secs))
	}
}

// TestUnitsMultipleHeadingsInOneChunk reproduces a real defect found in the
// EEA regulation corpus: a chunker can pack several short back-to-back
// headings (end of one article, a whole one-line chapter heading, start of
// the next article) into a single physical chunk. Only recognizing the first
// heading per chunk silently merged "Chapter 10 Evaluations" into the
// preceding article's unit, making the tool report a chapter that exists in
// both documents as present "only in" one of them.
func TestUnitsMultipleHeadingsInOneChunk(t *testing.T) {
	d := unitDoc("x",
		"## Article 9.8 Transparency and availability of documents\nsome closing text about audit trails.\n\n## Chapter 10 Evaluations\n\n## Article 10.",
		"1 National evaluations\nBeneficiary States shall carry out evaluations.",
	)
	us := Units(d)
	var headings []string
	for _, u := range us {
		headings = append(headings, u.Heading)
	}
	want := []string{
		"Article 9.8 Transparency and availability of documents",
		"Chapter 10 Evaluations",
		"Article 10. 1 National evaluations",
	}
	if len(us) != len(want) {
		t.Fatalf("got %d units %v, want %d %v", len(us), headings, len(want), want)
	}
	for i, h := range want {
		if us[i].Heading != h {
			t.Errorf("unit %d heading = %q, want %q", i, us[i].Heading, h)
		}
	}
	// The chapter heading unit and the article that follows it must each cite
	// the chunk they actually came from (both are fragments of chunk 0 here,
	// except the cross-chunk-repaired last article which pulls its number from
	// chunk 1 but is still anchored to chunk 0's citation).
	if us[1].Secs[0].ChunkID != d.Sections[0].ChunkID {
		t.Fatalf("Chapter 10 unit should cite chunk 0, got chunk id %d", us[1].Secs[0].ChunkID)
	}
}

func TestAlignUnitsKeyAnchorsAndOrphans(t *testing.T) {
	a := unitDoc("old",
		"## Article 1.1 Subject\nsame text",
		"## Article 2.1 Priority sectors\nold priorities body",
		"## Article 9.9 Removed thing\ngone in new",
	)
	b := unitDoc("new",
		"## Article 1.1 Subject\nsame text",
		"## Article 2.1 Thematic priorities\nnew priorities body",
		"## Article 3.3 Brand new thing\nadded",
	)
	// Orthogonal vectors for the removed/added units so the embedding fallback
	// cannot pair them.
	a.Sections[2].Vec = vec(1, 0)
	b.Sections[2].Vec = vec(0, 1)
	r := AlignUnits(a, b, "versions", 0.6)
	if r.Identical != 1 {
		t.Fatalf("Article 1.1 should be identical, got %d", r.Identical)
	}
	c := r.Counts()
	if c[Changed] != 1 || c[OnlyA] != 1 || c[OnlyB] != 1 {
		t.Fatalf("want 1 changed + 1 only_a + 1 only_b, got %v", c)
	}
	for _, p := range r.Pairs {
		if p.Kind == Changed && (p.A.Key != "article 2.1" || p.B.Key != "article 2.1") {
			t.Fatalf("changed pair should anchor on article 2.1: %+v", p)
		}
	}
}

// TestAlignUnitsTitleBeatsHigherCosineDecoy reproduces a real defect found in
// the EEA regulation test corpus: a renumbered article ("Cooperation
// Committee" 4.4 -> 4.3) never reaches the key-anchor path (the numbers
// differ), and in formulaic regulatory text a same-boilerplate but
// different-topic article can score a higher raw cosine similarity than the
// true, same-titled counterpart. Title overlap must win the match regardless.
func TestAlignUnitsTitleBeatsHigherCosineDecoy(t *testing.T) {
	a := unitDoc("old",
		"## Article 4.4 Cooperation Committee\nThe Programme Operator shall establish a Cooperation Committee.",
		"## Article 4.5 Donor partnership projects\nThe Programme Operator shall encourage such partnerships.",
	)
	b := unitDoc("new",
		"## Article 4.3 Cooperation Committee\nThe Programme Operator shall establish a Cooperation Committee with expanded membership.",
	)
	// The decoy (Donor partnership projects) gets the higher raw cosine; the
	// true match (Cooperation Committee) gets a lower cosine but shares a title.
	a.Sections[0].Vec = vec(0.8, 0.6) // Cooperation Committee (old)
	a.Sections[1].Vec = vec(1, 0)     // Donor partnership projects (old) — closer to b below
	b.Sections[0].Vec = vec(0.99, 0.14)

	r := AlignUnits(a, b, "versions", 0.5)
	c := r.Counts()
	if c[Changed] != 1 || c[OnlyA] != 1 {
		t.Fatalf("want 1 changed + 1 only_a, got %v", unitKinds(r))
	}
	for _, p := range r.Pairs {
		if p.Kind == Changed {
			if p.A.Key != "article 4.4" || p.B.Key != "article 4.3" {
				t.Fatalf("Cooperation Committee should match despite the renumbering and a higher-cosine decoy: got A=%s B=%s",
					p.A.Key, p.B.Key)
			}
		}
		if p.Kind == OnlyA && p.A.Key != "article 4.5" {
			t.Fatalf("the decoy (Donor partnership projects) should be the one left orphaned, got %s", p.A.Key)
		}
	}
}

func unitKinds(r *UnitResult) []Kind {
	out := make([]Kind, len(r.Pairs))
	for i, p := range r.Pairs {
		out[i] = p.Kind
	}
	return out
}

// TestAlignUnitsSameKeyDifferentTitlesDoesNotStealSlot reproduces a real
// defect found in the EEA regulation corpus: article renumbering can put two
// UNRELATED articles at the same number across versions (old 12.6 "Reporting
// on progress" vs new 12.6 "Complaint mechanism"), while the true match for
// "Complaint mechanism" is old 12.7. Same-key auto-matching must not force
// the coincidental same-number pairing through on cosine alone when real,
// non-overlapping titles are available — it must release both to the pool.
func TestAlignUnitsSameKeyDifferentTitlesDoesNotStealSlot(t *testing.T) {
	a := unitDoc("old",
		"## Article 12.6 Reporting on progress regarding already reported irregularities\nprogress report text",
		"## Article 12.7 Complaint mechanism\nthe beneficiary state shall establish a complaint mechanism",
	)
	b := unitDoc("new",
		"## Article 12.6 Complaint mechanism\nthe beneficiary state shall establish a complaint mechanism with updated rules",
	)
	// Old 12.6 and new 12.6 share a number but must NOT match on that alone:
	// give old 12.6 a vector close to new 12.6's (the trap) and old 12.7 a
	// vector that is comparatively worse, so only the title-priority fix
	// forces the correct pairing.
	a.Sections[0].Vec = vec(1, 0.05)  // old "Reporting on progress" — the decoy
	a.Sections[1].Vec = vec(0.9, 0.3) // old "Complaint mechanism" — the true match
	b.Sections[0].Vec = vec(1, 0)     // new "Complaint mechanism"

	r := AlignUnits(a, b, "versions", 0.5)
	c := r.Counts()
	if c[Changed] != 1 || c[OnlyA] != 1 {
		t.Fatalf("want 1 changed + 1 only_a, got %v", unitKinds(r))
	}
	for _, p := range r.Pairs {
		if p.Kind == Changed && p.A.Key != "article 12.7" {
			t.Fatalf("Complaint mechanism (new) should match old 12.7, not the same-numbered decoy: matched %s instead", p.A.Key)
		}
		if p.Kind == OnlyA && p.A.Key != "article 12.6" {
			t.Fatalf("the decoy (Reporting on progress) should be the one left orphaned, got %s", p.A.Key)
		}
	}
}

// TestUnitsMergesNumberOnlyHeadingWithFollowingTitle reproduces a real defect
// found in the EEA regulation corpus, using the exact real chunk boundaries:
// "## Article 9. 5 Forecast ...\n\n## Article 9." ends one chunk, and the next
// chunk continues "6 Use of the euro\n\n...\n\n## Article 9." (a second
// truncation), and the one after that continues "7\n\n## Interest\n\n- Any
// interest...". continueHeading correctly grabs "7" from the first line, but
// never sees that "## Interest" two lines later is the article's real title —
// so the numbered heading ends up empty and the substantive text ends up
// under a keyless "Interest" heading. The two must merge into one unit.
func TestUnitsMergesNumberOnlyHeadingWithFollowingTitle(t *testing.T) {
	d := unitDoc("x",
		"## Article 9. 5 Forecast of likely payment applications\nbody text about forecasts.\n\n## Article 9.",
		"6 Use of the euro\namounts shall be denominated in euro.\n\n## Article 9.",
		"7\n\n## Interest\n\n- Any interest generated on the following bank accounts shall be regarded as a resource for the FMC.",
	)
	us := Units(d)
	var interestUnit *Unit
	for i, u := range us {
		if strings.Contains(u.Heading, "Interest") {
			interestUnit = &us[i]
		}
	}
	if interestUnit == nil {
		t.Fatalf("no merged Interest unit found; units: %+v", us)
	}
	if interestUnit.Heading != "Article 9. 7 Interest" {
		t.Fatalf("merged heading = %q, want \"Article 9. 7 Interest\"", interestUnit.Heading)
	}
	if interestUnit.Key != "article 9.7" {
		t.Fatalf("merged key = %q, want \"article 9.7\"", interestUnit.Key)
	}
	if !strings.Contains(interestUnit.Text(), "resource for the FMC") {
		t.Fatalf("merged unit lost the real body text: %q", interestUnit.Text())
	}
	// No leftover empty "Article 9. 7" unit should survive alongside it.
	for _, u := range us {
		if u.Heading == "Article 9. 7" {
			t.Fatalf("the empty number-only unit should have been merged away, found: %+v", u)
		}
	}
}

// TestUnitsDoesNotMergeGenuineShortArticle guards against over-merging: a
// number-only heading whose body is real (non-numeric) text must NOT be
// merged into whatever heading happens to follow it.
func TestUnitsDoesNotMergeGenuineShortArticle(t *testing.T) {
	d := unitDoc("x",
		"## Article 5.9 Waiver\nThis Article shall apply mutatis mutandis.",
		"## Article 5.10 Force majeure\nNeither party shall be liable for delays caused by force majeure.",
	)
	us := Units(d)
	if len(us) != 2 {
		t.Fatalf("want 2 separate units (both have real bodies), got %d: %+v", len(us), us)
	}
	if us[0].Heading != "Article 5.9 Waiver" || us[1].Heading != "Article 5.10 Force majeure" {
		t.Fatalf("headings should be untouched: %+v", us)
	}
}

// TestBestSemanticMatchCatchesFullyRewordedMove reproduces the residual gap
// title-overlap and literal text search can't close: a section moved to a
// different chapter under a heading with ZERO shared vocabulary with the
// original ("External monitoring" -> "Third-party oversight"). Only the
// embedding still connects them.
func TestBestSemanticMatchCatchesFullyRewordedMove(t *testing.T) {
	orphan := Unit{Heading: "Article 11.1 External monitoring", Secs: []Section{
		{Text: "the FMC may select programmes for external monitoring", Vec: vec(1, 0.1)},
	}}
	others := []Unit{
		{Heading: "Article 10.3 Third-party oversight", Secs: []Section{
			{Text: "the FMC may select programmes for oversight by external parties", Vec: vec(0.95, 0.15)},
		}},
		{Heading: "Article 2.1 Thematic priorities", Secs: []Section{
			{Text: "completely unrelated content about funding priorities", Vec: vec(-1, 0.5)},
		}},
	}
	best, sim := BestSemanticMatch(orphan, others, 0.8)
	if best == nil {
		t.Fatal("want a semantic match above the floor, got nil")
	}
	if best.Heading != "Article 10.3 Third-party oversight" {
		t.Fatalf("matched wrong unit: %q (sim %.2f)", best.Heading, sim)
	}
}

func TestBestSemanticMatchNoneBelowFloor(t *testing.T) {
	orphan := Unit{Heading: "x", Secs: []Section{{Text: "t", Vec: vec(1, 0)}}}
	others := []Unit{{Heading: "y", Secs: []Section{{Text: "t", Vec: vec(0, 1)}}}}
	if best, _ := BestSemanticMatch(orphan, others, 0.8); best != nil {
		t.Fatalf("want no match below the floor, got %q", best.Heading)
	}
}

func TestAlignUnitsEmbeddingFallback(t *testing.T) {
	// No shared keys; the units must match by embedding similarity.
	a := unitDoc("a", "## Section One\nretention rules text")
	a.Sections[0].Vec = vec(1, 0)
	b := unitDoc("b", "## Paragraph Alpha\nrecords keeping text")
	b.Sections[0].Vec = vec(1, 0.05)
	r := AlignUnits(a, b, "topical", 0.8)
	if c := r.Counts(); c[Matched] != 1 {
		t.Fatalf("want 1 matched by embedding, got %v", c)
	}
}
