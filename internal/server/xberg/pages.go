package xberg

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// This file derives the page mapping — the rune offset at which each page of
// the converted text begins, pageStarts[i] for page i+1, matching pdfextract's
// and docling's convention — from an xberg extraction result.
//
// There are two sources for it, and they are not equally trustworthy:
//
//   - Page markers. With pages.insert_page_markers, xberg's pipeline injects a
//     marker block per page, and its Markdown/Djot renderers carry markers
//     through verbatim as RawBlock elements. Marker positions are therefore
//     positions in the *rendered* text, which is what we index.
//   - metadata.pages.boundaries. Byte ranges the extractor recorded over the
//     content string. Cheap and always present for paged formats, but computed
//     in the extraction pipeline, so on a rendered (Markdown) output format
//     they can drift out of alignment with the text we actually store.
//
// So markers are primary and boundaries are the fallback. Both paths are
// exercised by cmd/extracttest, which cross-checks them against each other.

// markerPrefix and markerSuffix bracket the page markers we ask xberg to
// insert. This is xberg's own default marker_format with the surrounding blank
// lines dropped: the pipeline trims the format before emitting it as a block,
// so the rendered marker is exactly "<!-- PAGE 12 -->" on its own line.
const (
	markerPrefix = "<!-- PAGE "
	markerSuffix = " -->"
)

// markerFormat is what we send in pages.marker_format. The blank lines keep
// markers as standalone blocks in plain-text output; renderers trim them.
const markerFormat = "\n\n" + markerPrefix + "{page_num}" + markerSuffix + "\n\n"

// stripPageMarkers removes every page marker line from text and returns the
// cleaned text plus the rune offset at which each page begins in it.
//
// A marker announces the page that follows it, so the offset recorded for that
// page is where the text after the marker starts. Pages that the extractor
// never announced (a marker for page 5 following one for page 3, which happens
// when a page holds no extractable text) are recorded at the same offset as the
// next page that was announced, so a chunk landing there cites the nearest real
// page rather than nothing.
//
// ok is false when the text carries no markers at all, which means marker
// insertion did not survive — the caller falls back to byte boundaries.
func stripPageMarkers(text string) (clean string, pageStarts []int, ok bool) {
	if !strings.Contains(text, markerPrefix) {
		return text, nil, false
	}

	var sb strings.Builder
	sb.Grow(len(text))
	runeOff := 0
	// starts maps a 1-based page number to its rune offset. Sparse: only pages
	// that were actually announced appear.
	starts := map[int]int{}
	maxPage := 0

	rest := text
	for {
		i := strings.Index(rest, markerPrefix)
		if i < 0 {
			break
		}
		j := strings.Index(rest[i+len(markerPrefix):], markerSuffix)
		if j < 0 {
			break
		}
		numStr := rest[i+len(markerPrefix) : i+len(markerPrefix)+j]
		page, valid := atoiPositive(numStr)
		if !valid {
			// Not one of our markers (an HTML comment that happens to share the
			// prefix): keep it in the text and move past it.
			sb.WriteString(rest[:i+len(markerPrefix)])
			runeOff += utf8.RuneCountInString(rest[:i+len(markerPrefix)])
			rest = rest[i+len(markerPrefix):]
			continue
		}

		// Emit everything before the marker, then drop the marker and the blank
		// line it sits on so the stored text reads as if it were never there.
		before := trimTrailingBlank(rest[:i])
		sb.WriteString(before)
		runeOff += utf8.RuneCountInString(before)

		rest = trimLeadingBlank(rest[i+len(markerPrefix)+j+len(markerSuffix):])

		// Separate pages the way the built-in PDF path does, except at the very
		// start where there is nothing to separate from.
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
			runeOff += 2
		}
		if _, seen := starts[page]; !seen {
			starts[page] = runeOff
		}
		maxPage = max(maxPage, page)
	}
	sb.WriteString(rest)

	if maxPage == 0 {
		return text, nil, false
	}

	// Densify: every page from 1..maxPage needs an entry, since pageFor indexes
	// positionally. Unannounced pages inherit the next announced page's offset.
	pageStarts = make([]int, maxPage)
	next := runeOff + utf8.RuneCountInString(rest)
	for p := maxPage; p >= 1; p-- {
		if off, seen := starts[p]; seen {
			next = off
		}
		pageStarts[p-1] = next
	}
	return sb.String(), pageStarts, true
}

// boundaryStarts converts xberg's byte-offset page boundaries over content into
// rune offsets. Boundaries arrive in no guaranteed order and may be sparse, so
// they are sorted by page and densified the same way markers are.
func boundaryStarts(content string, bs []pageBoundary) []int {
	if len(bs) == 0 {
		return nil
	}
	sorted := make([]pageBoundary, 0, len(bs))
	for _, b := range bs {
		// Defend against offsets that do not address this string: a boundary
		// past the end (or mid-rune) would otherwise slice a panic or a bogus
		// count out of it.
		if b.ByteStart <= len(content) && b.PageNumber > 0 {
			sorted = append(sorted, b)
		}
	}
	if len(sorted) == 0 {
		return nil
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PageNumber < sorted[j].PageNumber })

	maxPage := sorted[len(sorted)-1].PageNumber
	starts := map[int]int{}
	for _, b := range sorted {
		if _, seen := starts[b.PageNumber]; !seen {
			starts[b.PageNumber] = utf8.RuneCountInString(content[:b.ByteStart])
		}
	}
	out := make([]int, maxPage)
	next := utf8.RuneCountInString(content)
	for p := maxPage; p >= 1; p-- {
		if off, seen := starts[p]; seen {
			next = off
		}
		out[p-1] = next
	}
	return out
}

// atoiPositive parses a run of ASCII digits. Deliberately strict: no sign, no
// spaces, no empty string, so a stray "<!-- PAGE break -->" is not read as one
// of our markers.
func atoiPositive(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, n > 0
}

// trimTrailingBlank drops the whitespace run that ends s, so removing a marker
// does not leave the blank lines that surrounded it behind.
func trimTrailingBlank(s string) string { return strings.TrimRight(s, " \t\r\n") }

// trimLeadingBlank drops the whitespace run that starts s.
func trimLeadingBlank(s string) string { return strings.TrimLeft(s, " \t\r\n") }
