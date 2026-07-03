package api

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/ivoras/harlequin/internal/server/auth"
	"github.com/ivoras/harlequin/internal/server/docalign"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// The web side-by-side comparison view fetches a full deterministic alignment
// of two documents in one request (unlike the agent's align_docs tool, which
// serves the same result in small batches for the LLM).

type alignSectionJSON struct {
	ChunkID int64  `json:"chunk_id"`
	Ord     int    `json:"ord"`
	Page    int    `json:"page,omitempty"`
	Text    string `json:"text"`
}

type alignPairJSON struct {
	Kind       string             `json:"kind"`
	Similarity float64            `json:"similarity,omitempty"`
	A          []alignSectionJSON `json:"a"`
	B          []alignSectionJSON `json:"b"`
}

type alignDocJSON struct {
	Ref      string `json:"ref"` // scoped id, e.g. "u.2"
	Title    string `json:"title"`
	Scope    string `json:"scope"`
	Sections int    `json:"sections"`
}

type alignResponseJSON struct {
	Mode          string          `json:"mode"`
	MinSimilarity float64         `json:"min_similarity"`
	Identical     int             `json:"identical"`
	A             alignDocJSON    `json:"a"`
	B             alignDocJSON    `json:"b"`
	Pairs         []alignPairJSON `json:"pairs"`
}

// handleAlignDocuments serves
// GET /documents/align?a=u.2&b=u.3&mode=versions[&min_similarity=0.55][&project=N]
func (s *Server) handleAlignDocuments(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFromContext(r.Context())
	mode := r.URL.Query().Get("mode")
	if mode != "versions" && mode != "topical" {
		writeErr(w, http.StatusBadRequest, `mode must be "versions" or "topical"`)
		return
	}
	minSim := 0.55
	if v := r.URL.Query().Get("min_similarity"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 || f >= 1 {
			writeErr(w, http.StatusBadRequest, "min_similarity must be between 0 and 1 (exclusive)")
			return
		}
		minSim = f
	}
	projectID, _ := strconv.ParseInt(r.URL.Query().Get("project"), 10, 64)

	docA, status, msg := s.loadAlignDoc(r.Context(), u, r.URL.Query().Get("a"), projectID)
	if msg != "" {
		writeErr(w, status, msg)
		return
	}
	docB, status, msg := s.loadAlignDoc(r.Context(), u, r.URL.Query().Get("b"), projectID)
	if msg != "" {
		writeErr(w, status, msg)
		return
	}
	if len(docA.Sections) == 0 || len(docB.Sections) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "both documents must have indexed sections")
		return
	}

	var res *docalign.Result
	if mode == "versions" {
		res = docalign.AlignVersions(docA, docB, minSim)
	} else {
		var err error
		res, err = docalign.AlignTopical(docA, docB, minSim)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}

	out := alignResponseJSON{
		Mode:          mode,
		MinSimilarity: minSim,
		Identical:     res.Identical,
		A:             alignDocMeta(docA),
		B:             alignDocMeta(docB),
		Pairs:         make([]alignPairJSON, 0, len(res.Pairs)),
	}
	for _, p := range res.Pairs {
		out.Pairs = append(out.Pairs, alignPairJSON{
			Kind:       string(p.Kind),
			Similarity: p.Similarity,
			A:          alignSections(p.A),
			B:          alignSections(p.B),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func alignDocMeta(d *docalign.Doc) alignDocJSON {
	return alignDocJSON{
		Ref:      scopeRefLetter(d.Scope) + "." + strconv.FormatInt(d.ID, 10),
		Title:    d.Title,
		Scope:    d.Scope,
		Sections: len(d.Sections),
	}
}

func alignSections(secs []docalign.Section) []alignSectionJSON {
	out := make([]alignSectionJSON, 0, len(secs))
	for _, s := range secs {
		out = append(out, alignSectionJSON{ChunkID: s.ChunkID, Ord: s.Ord, Page: s.Page, Text: s.Text})
	}
	return out
}

func scopeRefLetter(scope string) string {
	switch scope {
	case documents.ScopePersonal:
		return "u"
	case documents.ScopeProject:
		return "p"
	default:
		return "s"
	}
}

// loadAlignDoc resolves a scoped document ref ("u.N", "s.N", "p.N") for the
// requesting user and loads its sections. Non-empty msg is the HTTP error.
func (s *Server) loadAlignDoc(ctx context.Context, u *types.User, ref string, projectID int64) (doc *docalign.Doc, status int, msg string) {
	parts := strings.Split(strings.TrimSpace(ref), ".")
	if len(parts) != 2 {
		return nil, http.StatusBadRequest, "document ref must be u.N, s.N or p.N: " + ref
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return nil, http.StatusBadRequest, "bad document id in " + ref
	}
	switch parts[0] {
	case "u":
		err = s.Storage.WithUser(ctx, u.ID, func(udb *sql.DB) error {
			var e error
			doc, e = docalign.LoadDoc(ctx, udb, documents.ScopePersonal, id)
			return e
		})
	case "s":
		doc, err = docalign.LoadDoc(ctx, s.Docs.SharedDB(), documents.ScopeShared, id)
	case "p":
		if member, _ := s.Projects.IsMember(ctx, projectID, u.ID); !member {
			return nil, http.StatusForbidden, "not a member of this project"
		}
		err = s.Storage.WithProjectReadOnly(ctx, projectID, func(pdb *sql.DB) error {
			var e error
			doc, e = docalign.LoadDoc(ctx, pdb, documents.ScopeProject, id)
			return e
		})
	default:
		return nil, http.StatusBadRequest, "unknown document scope in " + ref
	}
	if err != nil || doc == nil {
		return nil, http.StatusNotFound, "document " + ref + " not found"
	}
	return doc, 0, ""
}
