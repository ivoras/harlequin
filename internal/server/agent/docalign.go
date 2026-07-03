package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ivoras/harlequin/internal/server/docalign"
	"github.com/ivoras/harlequin/internal/server/documents"
)

// align_docs batching: a result batch stops after batchMaxPairs pairs or once
// batchMaxRunes of section text has been rendered (whichever first), so one
// tool result stays small enough for the model to analyse pair by pair. A
// single side is clipped at sideMaxRunes.
const (
	alignBatchMaxPairs = 4
	alignBatchMaxRunes = 6000
	alignSideMaxRunes  = 2400
	alignDefaultMinSim = 0.55
)

// alignDocsEntry is the align_docs tool: deterministic two-document alignment
// (versions diff or topical matching) served to the model in cursor batches.
func (a *Agent) alignDocsEntry() toolEntry {
	return toolEntry{
		def: fnTool("align_docs", `Align two documents from the document corpus so their differences can be analysed pair by pair. Use mode "versions" when the documents are two revisions of the same text (identical sections are skipped; you only see what changed), and mode "topical" when they are different texts about the same subject (sections are paired by meaning; sections without a counterpart are reported as present in only one document).
Documents are referenced by scoped id: u.N (personal), s.N (shared), p.N (project), exactly as search_docs shows them ("doc u.3"); a bare N works when it is unambiguous.
The result is a batch of aligned pairs. Analyse each pair (state the substantive difference, or that there is none), then call align_docs again with the returned cursor for the next batch until no pairs remain. The alignment is deterministic: the same arguments always produce the same pairs and cursors.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"doc_a": map[string]any{"type": "string", "description": "First document, e.g. \"u.12\", \"s.3\", \"p.7\" (or a bare id if unambiguous)"},
				"doc_b": map[string]any{"type": "string", "description": "Second document"},
				"mode":  map[string]any{"type": "string", "enum": []string{"versions", "topical"}},
				"cursor": map[string]any{"type": "integer",
					"description": "1-based index of the first pair to return (from the previous call's result); omit to start"},
				"min_similarity": map[string]any{"type": "number",
					"description": "Cosine similarity floor for pairing sections (default 0.55). Lower it if too much lands in only_a/only_b, raise it if unrelated sections get paired."},
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
		},
	}
}

// renderAlignment formats the batch starting at the 1-based cursor.
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

// renderSide prints one side of a pair: its chunk citations and (clipped) text,
// or a note that the side has no counterpart.
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
	switch scope {
	case documents.ScopePersonal:
		return "u"
	case documents.ScopeProject:
		return "p"
	default:
		return "s"
	}
}

// loadDocRef resolves a model-supplied document reference ("u.12", "s.3",
// "p.7", or a bare id) against the scopes available to this session and loads
// the document. A non-empty second return value is a tool-facing error.
func (a *Agent) loadDocRef(ctx context.Context, rc *runContext, ref string) (*docalign.Doc, string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, "error: doc_a and doc_b are required (scoped document ids like u.12, s.3 or p.7)"
	}
	scopes := a.Docs.ScopesFor(rc.userDB, rc.projectDB)
	byLetter := map[string]documents.ScopeDB{}
	for _, sc := range scopes {
		byLetter[scopeLetter(sc.Scope)] = sc
	}
	// Scoped form: <letter>.<id> (tolerate the chunk-id style "d.u.N" prefix).
	if parts := strings.Split(strings.TrimPrefix(ref, "d."), "."); len(parts) == 2 {
		sc, ok := byLetter[parts[0]]
		if !ok {
			return nil, fmt.Sprintf("error: unknown document scope %q in %q (use u.N, s.N or p.N)", parts[0], ref)
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Sprintf("error: bad document id in %q", ref)
		}
		d, err := docalign.LoadDoc(ctx, sc.DB, sc.Scope, id)
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

// argFloat reads a float tool argument (JSON numbers decode as float64).
func argFloat(args map[string]any, key string, def float64) float64 {
	if v, ok := args[key].(float64); ok {
		return v
	}
	return def
}
