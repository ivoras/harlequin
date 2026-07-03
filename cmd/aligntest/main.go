// Command aligntest exercises the docalign engine end to end against a real
// sqlite corpus and the configured embeddings provider (no chat LLM): it
// ingests two revisions of one text and two different texts about the same
// subject, runs both alignment modes, prints the aligned pairs, and asserts
// the expected shape (identical anchors skipped, the edit surfaces as a
// changed pair, topic orphans are reported per side). It reads the same
// server.yaml + .env as the server, so it uses exactly the configured
// embedding model. Run from the repo root:
//
//	CGO_ENABLED=1 CGO_CFLAGS="-I$(pwd)/third_party/sqlite/include" \
//	  go run -tags sqlite_fts5 ./cmd/aligntest [-config server.yaml] [-minsim 0.55]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/docalign"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", a...)
	os.Exit(1)
}

// Two revisions of the same text: v2 rewrites the fee clause, drops the paper
// filings clause, and adds a whistleblower clause.
const specV1 = `Article 1. Scope. This regulation applies to all providers of payment services operating within the internal market. It covers credit transfers, direct debits and card-based payments, whether initiated at a terminal or remotely over the internet.

Article 2. Fees. The fee for a cross-border credit transfer shall not exceed 1.20 EUR per transaction. Providers shall publish their full fee schedules and update them at least once per calendar year.

Article 3. Filings. Providers shall submit quarterly reports on paper forms to the national supervisory authority, using the templates published in Annex II. Late submissions incur an administrative surcharge.

Article 4. Supervision. National authorities shall cooperate with the central supervisory board and exchange information about infringements without undue delay. Joint inspections may be organised where cross-border activity is significant.`

const specV2 = `Article 1. Scope. This regulation applies to all providers of payment services operating within the internal market. It covers credit transfers, direct debits and card-based payments, whether initiated at a terminal or remotely over the internet.

Article 2. Fees. The fee for a cross-border credit transfer shall not exceed 0.50 EUR per transaction, and shall be waived entirely for transfers below 20 EUR. Providers shall publish their full fee schedules and update them at least twice per calendar year.

Article 4. Supervision. National authorities shall cooperate with the central supervisory board and exchange information about infringements without undue delay. Joint inspections may be organised where cross-border activity is significant.

Article 5. Whistleblowers. Persons reporting infringements of this regulation in good faith shall be protected from retaliation, and providers shall maintain confidential internal reporting channels for their staff.`

// Two different texts about the same subject: each covers retention and
// penalties (should match), and each has one topic the other lacks.
const lawA = `Section 10. Data retention. Telecommunications operators must retain traffic metadata for a period of six months and destroy it irreversibly once the period expires. Retained data may be accessed only with a judicial warrant issued by a competent court.

Section 11. Penalties. An operator that unlawfully discloses retained data commits an offence punishable by a fine of up to two million euros or two percent of annual turnover, whichever is higher.

Section 12. Roadside cameras. Images captured by automated traffic cameras are stored for thirty days and are available to the traffic police for the investigation of offences committed on public roads.`

const lawB = `Paragraph 7. Storage of communications records. Carriers shall keep records of message routing for no longer than ninety days, after which the records must be erased. Access to the stored records requires prior authorisation from an independent oversight commission.

Paragraph 8. Sanctions. Whoever discloses stored communications records without authorisation shall be liable to imprisonment of up to two years or a monetary penalty proportional to the gravity of the breach.

Paragraph 9. Encrypted services. Providers of end-to-end encrypted messaging are exempt from the record-keeping duty for message content, but must still record subscriber registration details at sign-up.`

func main() {
	configPath := flag.String("config", "server.yaml", "path to server config YAML")
	minSim := flag.Float64("minsim", 0.55, "similarity floor for pairing sections")
	flag.Parse()
	ctx := context.Background()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fail("config: %v", err)
	}
	if cfg.Embeddings.BaseURL == "" || cfg.Embeddings.Model == "" {
		fail("no embeddings provider configured (embeddings.base_url/model in %s)", *configPath)
	}
	embedder := embed.New(cfg.Embeddings.BaseURL, cfg.Embeddings.APIKey, cfg.Embeddings.Model,
		cfg.Embeddings.Dim, cfg.Embeddings.QueryPrefix, cfg.Embeddings.DocPrefix)
	if _, err := embedder.Embed(ctx, []string{"connectivity probe"}); err != nil {
		fail("embedding provider unreachable (%s @ %s): %v", cfg.Embeddings.Model, cfg.Embeddings.BaseURL, err)
	}

	dir, _ := os.MkdirTemp("", "aligntest")
	defer os.RemoveAll(dir)
	shared, err := db.Open(filepath.Join(dir, "shared.db"), db.Shared, cfg.Embeddings.Dim)
	if err != nil {
		fail("open shared db: %v", err)
	}
	defer shared.Close()

	store := documents.NewStore(shared, embedder)
	// Semantic chunking gives section-shaped chunks, which is what alignment
	// wants; fall back to the config's gate when set.
	cc := documents.ChunkConfig{Strategy: "semadj"}
	if cfg.Documents.SemAdjGate > 0 {
		cc.SemAdjGate = cfg.Documents.SemAdjGate
	}
	store.SetChunkConfig(cc)

	ingest := func(title, content string) *docalign.Doc {
		d, err := store.Ingest(ctx, types.CreateDocumentRequest{Title: title, Content: content}, 1)
		if err != nil {
			fail("ingest %q: %v", title, err)
		}
		loaded, err := docalign.LoadDoc(ctx, shared, documents.ScopeShared, d.ID)
		if err != nil {
			fail("load %q: %v", title, err)
		}
		fmt.Printf("ok: ingested %q as s.%d (%d sections)\n", title, d.ID, len(loaded.Sections))
		return loaded
	}
	v1 := ingest("payments regulation v1", specV1)
	v2 := ingest("payments regulation v2", specV2)
	a := ingest("retention law A", lawA)
	b := ingest("retention law B", lawB)

	// --- versions mode ---
	vres := docalign.AlignVersions(v1, v2, *minSim)
	fmt.Printf("\nversions: %d identical skipped, %d pairs %v\n", vres.Identical, len(vres.Pairs), summarize(vres))
	printPairs(vres)
	if vres.Identical == 0 {
		fail("versions: the unchanged articles should anchor as identical")
	}
	if len(vres.Pairs) == 0 {
		fail("versions: the fee rewrite must surface as at least one pair")
	}
	// The fee sentence must pair with its rewritten counterpart. (Match on the
	// sentence stem, not the amount: the sentence splitter cuts inside "1.20",
	// so the number can land in an adjacent chunk.)
	const feeStem = "fee for a cross-border credit transfer"
	feeSeen := false
	for _, p := range vres.Pairs {
		if p.Kind == docalign.Changed && sideHas(p.A, feeStem) && sideHas(p.B, feeStem) {
			feeSeen = true
		}
	}
	if !feeSeen {
		fail("versions: the fee rewrite was not paired as changed")
	}
	fmt.Println("ok: versions mode anchors identical text and pairs the fee rewrite")

	// --- topical mode ---
	tres, err := docalign.AlignTopical(a, b, *minSim)
	if err != nil {
		fail("topical: %v", err)
	}
	fmt.Printf("\ntopical: %d pairs %v\n", len(tres.Pairs), summarize(tres))
	printPairs(tres)
	c := tres.Counts()
	if c[docalign.Matched] == 0 {
		fail("topical: retention/penalties sections should match across the laws")
	}
	if c[docalign.OnlyA] == 0 || c[docalign.OnlyB] == 0 {
		fail("topical: each law's exclusive topic should be reported as an orphan (only_a=%d only_b=%d)",
			c[docalign.OnlyA], c[docalign.OnlyB])
	}
	fmt.Println("ok: topical mode matches shared topics and reports per-side orphans")

	fmt.Println("PASS")
}

func summarize(r *docalign.Result) map[docalign.Kind]int { return r.Counts() }

func printPairs(r *docalign.Result) {
	for i, p := range r.Pairs {
		fmt.Printf("  %2d. %-7s sim=%.2f  A=%s | B=%s\n", i+1, p.Kind, p.Similarity, head(p.A), head(p.B))
	}
}

func head(secs []docalign.Section) string {
	if len(secs) == 0 {
		return "-"
	}
	t := strings.Join(strings.Fields(secs[0].Text), " ")
	if r := []rune(t); len(r) > 60 {
		t = string(r[:60]) + "…"
	}
	if len(secs) > 1 {
		t += fmt.Sprintf(" (+%d more)", len(secs)-1)
	}
	return t
}

func sideHas(secs []docalign.Section, needle string) bool {
	for _, s := range secs {
		if strings.Contains(s.Text, needle) {
			return true
		}
	}
	return false
}
