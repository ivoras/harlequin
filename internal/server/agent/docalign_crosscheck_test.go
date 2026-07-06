package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/docalign"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/shared/types"
)

type crossCheckFakeEmbedder struct{ dim int }

func (f crossCheckFakeEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i := range out {
		out[i] = make([]float32, f.dim)
	}
	return out, nil
}
func (f crossCheckFakeEmbedder) EmbedQuery(ctx context.Context, inputs []string) ([][]float32, error) {
	return f.Embed(ctx, inputs)
}
func (f crossCheckFakeEmbedder) Dim() int { return f.dim }

// TestAppendOrphanCrossCheck verifies the safety-net check itself, independent
// of whether the alignment engine happens to misclassify a pair: whatever
// produced an only_a/only_b pair (alignment quirks, PDF conversion noise,
// coincidental renumbering), the cross-check must authoritatively confirm or
// rule out that the orphan's heading text also exists in the other document,
// so a "removed"/"new" claim is never based on the classification alone. This
// guards against a live failure: the model asserted a section was "removed"
// without ever calling a separate verification tool, even when the skill
// instructed it to — making the check automatic closes that gap.
func TestAppendOrphanCrossCheck(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := db.Open(":memory:", db.Shared, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	store := documents.NewStore(sqlDB, crossCheckFakeEmbedder{dim: 4})

	// "Complaint mechanism" is present in both documents (the orphan
	// classification is assumed here, standing in for however it arose in
	// practice); "Genuinely gone" exists only in the old document.
	oldDoc, err := store.Ingest(ctx, docReq("## Article 12.7 Complaint mechanism\nThe Beneficiary State shall establish a complaint mechanism.\n\n## Article 9.1 Genuinely gone\nThis provision has no counterpart."), 1)
	if err != nil {
		t.Fatal(err)
	}
	newDoc, err := store.Ingest(ctx, docReq("## Article 12.6 Complaint mechanism\nThe Beneficiary State shall establish a complaint mechanism with updated rules."), 1)
	if err != nil {
		t.Fatal(err)
	}

	a := &Agent{Docs: store}
	docA, err := docalign.LoadDoc(ctx, sqlDB, "shared", oldDoc.ID)
	if err != nil {
		t.Fatal(err)
	}
	docB, err := docalign.LoadDoc(ctx, sqlDB, "shared", newDoc.ID)
	if err != nil {
		t.Fatal(err)
	}
	unitsA := docalign.Units(docA)
	var complaintUnit, goneUnit *docalign.Unit
	for i, u := range unitsA {
		switch {
		case strings.Contains(u.Heading, "Complaint mechanism"):
			complaintUnit = &unitsA[i]
		case strings.Contains(u.Heading, "Genuinely gone"):
			goneUnit = &unitsA[i]
		}
	}
	if complaintUnit == nil || goneUnit == nil {
		t.Fatalf("expected both test units to parse, got %+v", unitsA)
	}

	var sb strings.Builder
	a.appendOrphanCrossCheck(ctx, &sb, docalign.UnitPair{Kind: docalign.OnlyA, A: complaintUnit}, docA, sqlDB, docB, sqlDB)
	out := sb.String()
	if !strings.Contains(out, "WARNING") {
		t.Fatalf("cross-check should flag the renumbered Complaint mechanism as still present, got:\n%s", out)
	}
	if !strings.Contains(out, "d.s.") {
		t.Fatalf("cross-check warning should cite the real counterpart chunk, got:\n%s", out)
	}

	sb.Reset()
	a.appendOrphanCrossCheck(ctx, &sb, docalign.UnitPair{Kind: docalign.OnlyA, A: goneUnit}, docA, sqlDB, docB, sqlDB)
	out2 := sb.String()
	if strings.Contains(out2, "WARNING") {
		t.Fatalf("cross-check should NOT warn for genuinely absent content, got:\n%s", out2)
	}
	if !strings.Contains(out2, "not found") {
		t.Fatalf("cross-check should confirm genuine absence, got:\n%s", out2)
	}
}

func docReq(content string) types.CreateDocumentRequest {
	return types.CreateDocumentRequest{Title: "test doc", Content: content}
}
