package documents

import "testing"

// TestSplitSentencesDoesNotBreakBracketedAbbreviations reproduces a live bug:
// a save_doc report containing "[d.p. 3651]" was split into "...[d.p." and
// "3651]..." because the sentence splitter reads "period, space, digit" as a
// sentence boundary — indistinguishable, without this guard, from a real
// sentence ending right before a numbered list item. FullText then rejoined
// the pieces with a paragraph break, visibly mangling the citation in the
// browser. The fix is general (citeAbbrevRE matches any "[x.y." shape), not
// specific to the "d" (document) reference namespace, since other bracketed
// reference forms (e.g. memory ids) share the same shape.
func TestSplitSentencesDoesNotBreakBracketedAbbreviations(t *testing.T) {
	cases := []struct {
		name, text, abbrev string
	}{
		{"document citation", "renamed from Strategic Report [d.p. 3651] to Country Report.", "[d.p."},
		{"different letters", "see the related note [m.s. 42] for background.", "[m.s."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, s := range splitSentences(c.text) {
				if s.text == c.abbrev {
					t.Fatalf("citation abbreviation %q was split into its own sentence, from text %q", c.abbrev, c.text)
				}
			}
		})
	}
}
