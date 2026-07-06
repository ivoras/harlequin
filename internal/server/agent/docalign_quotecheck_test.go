package agent

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// TestVerifyCitedQuotes reproduces a real, confirmed live failure: the model
// quoted a real clause accurately but cited an adjacent, textually similar
// clause's chunk id instead of the one it actually came from ("comment rights
// broadened" text lived in one chunk; the report cited the neighbouring
// chunk about audit planning principles). A correct quote+citation pair must
// pass; a real quote cited to the wrong chunk must be caught.
func TestVerifyCitedQuotes(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := db.Open(":memory:", db.Shared, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	store := documents.NewStore(sqlDB, crossCheckFakeEmbedder{dim: 4})

	chunkOf := func(content string) int64 {
		doc, err := store.Ingest(ctx, types.CreateDocumentRequest{Title: "t", Content: content}, 1)
		if err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := sqlDB.QueryRow("SELECT id FROM doc_chunks WHERE document_id = ? ORDER BY ord LIMIT 1", doc.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	commentsChunk := chunkOf("## Article 11.2 Audits arranged by the FMC\nThe National Focal Point, the Programme Operator where relevant, and any other audited entities shall be given an opportunity to provide comments to an audit report before it is finalised.")
	principlesChunk := chunkOf("## Article 11.2.4 Principles\nWhen planning and carrying out audits, the FMC shall, where possible, take into account the principles laid out in Article 5.")

	a := &Agent{Docs: store}
	rc := &runContext{userDB: sqlDB}

	correct := `The report states "any other audited entities shall be given an opportunity to provide comments to an audit report before it is finalised" [d.s.` +
		strconv.FormatInt(commentsChunk, 10) + `].`
	if problems := a.VerifyCitedQuotes(ctx, rc, correct); len(problems) != 0 {
		t.Fatalf("correct citation should pass, got problems: %v", problems)
	}

	wrong := `The report states "any other audited entities shall be given an opportunity to provide comments to an audit report before it is finalised" [d.s.` +
		strconv.FormatInt(principlesChunk, 10) + `].`
	problems := a.VerifyCitedQuotes(ctx, rc, wrong)
	if len(problems) != 1 {
		t.Fatalf("wrong citation should be caught, got %d problems: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "audited entities") {
		t.Fatalf("problem message should quote the mismatched claim, got: %s", problems[0])
	}

	// A long quoted claim with NO citation at all must also be caught — this
	// is the exact live failure that survived the wrong-chunk check: three
	// verbatim quotes packed into one sentence with zero citations attached.
	uncited := `The report states "any other audited entities shall be given an opportunity to provide comments to an audit report before it is finalised" without citing anything.`
	problems = a.VerifyCitedQuotes(ctx, rc, uncited)
	if len(problems) != 1 {
		t.Fatalf("an uncited long quote should be caught, got %d problems: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "no [d.x.N] citation") {
		t.Fatalf("problem message should explain the missing citation, got: %s", problems[0])
	}

	// A short quoted word/phrase is exempt — not every quotation mark is a
	// verbatim document excerpt; short ones are typically just naming
	// something (a heading, a term) rather than citing a clause.
	shortQuote := `The report mentions "the FMC" in passing, without treating it as a verbatim excerpt.`
	if problems := a.VerifyCitedQuotes(ctx, rc, shortQuote); len(problems) != 0 {
		t.Fatalf("a short quoted phrase should be exempt from the citation requirement, got: %v", problems)
	}
}

