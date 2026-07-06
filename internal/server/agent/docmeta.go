package agent

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/llm"
)

// describeInputCap bounds how much document text the description prompt sees —
// the opening of a document identifies it; the rest just costs tokens.
const describeInputCap = 6000

const describeSystemPrompt = `You catalogue documents. Given a document's title and its opening text, reply with ONE line (at most 25 words) stating what the document is: its type, subject, issuing body if apparent, and any version/date/edition hints. No preamble, no quotes, no markdown.`

// DescribeDocument produces the one-line catalogue description stored with an
// ingested document, which lets the assistant resolve paraphrased references
// ("the new EEA regulation") against the document list. Best-effort: returns ""
// on any failure so ingestion never blocks on it. This is the synchronous
// at-upload attempt; it can lose to model contention — the caller should fall
// back to DescribeDocumentBackground.
func (a *Agent) DescribeDocument(ctx context.Context, title, text string) string {
	return a.describeOnce(ctx, title, text, 90*time.Second)
}

// DescribeDocumentBackground is DescribeDocument behind the background-LLM
// gate: it waits until no live turn or other background job (memory bridge)
// holds the model, and survives preemption. Use for post-upload repair.
func (a *Agent) DescribeDocumentBackground(ctx context.Context, title, text string) string {
	var desc string
	if !a.RunBackgroundLLM(ctx, func(jobCtx context.Context) {
		desc = a.describeOnce(jobCtx, title, text, 3*time.Minute)
	}) {
		return ""
	}
	return desc
}

func (a *Agent) describeOnce(ctx context.Context, title, text string, timeout time.Duration) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if r := []rune(text); len(r) > describeInputCap {
		text = string(r[:describeInputCap])
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, _, err := a.completeOnce(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: describeSystemPrompt},
			{Role: llm.RoleUser, Content: "Title: " + title + "\n\nOpening text:\n" + text},
		},
		Temperature: llm.Ptr(0.0),
	})
	if err != nil {
		log.Printf("documents: describe %q failed: %v", title, err)
		return ""
	}
	// One line, bounded; models sometimes add a second explanatory line.
	desc := strings.TrimSpace(out)
	if i := strings.IndexByte(desc, '\n'); i >= 0 {
		desc = strings.TrimSpace(desc[:i])
	}
	desc = strings.Trim(desc, `"`)
	if r := []rune(desc); len(r) > 220 {
		desc = string(r[:220]) + "…"
	}
	return desc
}
