// Package searchquery expands short document-search queries for hybrid retrieval.
package searchquery

import (
	"strings"
	"unicode"
)

const expandWordLimit = 4 // expand short queries only; longer ones stay literal

// abbrevExpansions maps colloquial tokens to formal/legal synonyms likely to
// appear in uploaded corpora (e.g. treaties say "seat", not "HQ").
var abbrevExpansions = map[string][]string{
	"hq": {"headquarters", "seat", "Brussels", "Strasbourg", "Luxembourg", "location"},
	"eu": {"European", "European Union"},
}

var focusedExpansionTerms = map[string]bool{
	"Brussels": true, "Strasbourg": true, "Luxembourg": true,
	"headquarters": true, "location": true,
}

// FTS builds an FTS5 MATCH expression. Short queries and any query containing a
// known abbreviation are widened to an OR of the user's terms plus synonyms so
// colloquial tokens (HQ) and acronyms (EU) still hit formal text (seat, European).
func FTS(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return query
	}
	words := words(query)
	if len(words) == 0 || len(words) > expandWordLimit {
		return query
	}
	terms, widened := collectTerms(words)
	if !widened && len(words) > 2 {
		return query
	}
	if len(terms) == 1 {
		return quoteTerm(terms[0])
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = quoteTerm(t)
	}
	return strings.Join(quoted, " OR ")
}

// FocusedFTS returns an OR query over location-specific expansion terms when the
// query was widened (e.g. HQ → Brussels); empty otherwise. Generic stems like
// "seat" and "European" are omitted because they match too broadly.
func FocusedFTS(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	words := words(query)
	if len(words) == 0 || len(words) > expandWordLimit {
		return ""
	}
	_, widened := collectTerms(words)
	if !widened {
		return ""
	}
	seen := map[string]bool{}
	var terms []string
	for _, w := range words {
		ex, ok := abbrevExpansions[strings.ToLower(w)]
		if !ok {
			continue
		}
		for _, e := range ex {
			if !focusedExpansionTerms[e] || seen[e] {
				continue
			}
			seen[e] = true
			terms = append(terms, quoteTerm(e))
		}
	}
	if len(terms) == 0 {
		return ""
	}
	return strings.Join(terms, " OR ")
}

// Embed appends synonym terms for short queries so the dense arm can match
// formal wording the user did not type (e.g. "EU HQ" → … seat Brussels).
func Embed(query string) string {
	query = strings.TrimSpace(query)
	words := words(query)
	if len(words) == 0 || len(words) > expandWordLimit {
		return query
	}
	terms, _ := collectTerms(words)
	return strings.Join(terms, " ")
}

func collectTerms(words []string) (terms []string, widened bool) {
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		k := strings.ToLower(t)
		if seen[k] {
			return
		}
		seen[k] = true
		terms = append(terms, t)
	}
	for _, w := range words {
		add(w)
		if ex, ok := abbrevExpansions[strings.ToLower(w)]; ok {
			widened = true
			for _, e := range ex {
				add(e)
			}
		}
	}
	if len(words) <= 2 {
		widened = true
	}
	return terms, widened
}

func words(query string) []string {
	var out []string
	for _, f := range strings.Fields(query) {
		f = strings.Trim(f, `"'`)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func quoteTerm(term string) string {
	term = strings.TrimSpace(term)
	if term == "" {
		return ""
	}
	if strings.Contains(term, " ") || strings.ContainsAny(term, `"'`) ||
		strings.IndexFunc(term, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_'
		}) >= 0 {
		return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	return term
}
