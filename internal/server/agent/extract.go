package agent

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/agent/memextract"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// docMemoryInputCap bounds how much imported-document text is fed to the memory
// distiller in one pass, so a large upload stays within the model's context and
// finishes within docMemoryTimeout. Extraction sees the leading portion (titles,
// summaries, who/what/when and key clauses usually live up front).
const docMemoryInputCap = 8000

// Timeouts: conversational turns are small; a document prompt is much larger, so
// it needs longer (prefill of several thousand tokens on a local model).
const (
	convMemoryTimeout = 60 * time.Second
	docMemoryTimeout  = 3 * time.Minute
)

// extractMemories asks the LLM to distill durable facts from a conversation turn
// and stores them. See distillAndStore for the shared core.
func (a *Agent) extractMemories(ctx context.Context, userID int64, userContent, assistantText string, turnWritten []string, canShareMemory bool) {
	convo := "User said: " + userContent + "\nAssistant said: " + assistantText
	a.distillAndStore(ctx, userID, memextract.Prompt, convo, turnWritten, canShareMemory, convMemoryTimeout)
}

// ExtractMemoriesFromText distills durable facts from a block of source text
// (e.g. an imported document) and stores them, reusing the same judge/dedup as
// conversational auto-extraction. Best-effort; intended to run in a goroutine.
// source is a short label (e.g. the document title) included for context.
func (a *Agent) ExtractMemoriesFromText(ctx context.Context, userID int64, source, text string, canShareMemory bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(text) > docMemoryInputCap {
		text = text[:docMemoryInputCap]
	}
	input := "Imported document"
	if source != "" {
		input += " (" + source + ")"
	}
	input += ":\n" + text
	n := a.distillAndStore(ctx, userID, memextract.DocumentPrompt, input, nil, canShareMemory, docMemoryTimeout)
	log.Printf("documents: memory bridge stored %d memory(ies) from %q for user %d", n, source, userID)
}

// distillAndStore runs the memory-extraction LLM over input, then stores each
// accepted, non-redundant candidate (downgrading shared->user when the caller
// can't write shared memory).
func (a *Agent) distillAndStore(ctx context.Context, userID int64, systemPrompt, input string, turnWritten []string, canShareMemory bool, timeout time.Duration) int {
	// The extraction call shares the background-LLM slot with the auto-titler:
	// one background completion at a time, started only while no live turn is
	// on the model. The LLM call gets its own deadline; storage (embedding +
	// insert) gets a separate one derived from the parent, so a slow extraction
	// can't starve the store step of time (which previously made every embed
	// time out).
	var text string
	var llmErr error
	if !a.runBackgroundLLM(ctx, func() {
		llmCtx, cancelLLM := context.WithTimeout(ctx, timeout)
		defer cancelLLM()
		text, _, llmErr = a.completeOnce(llmCtx, llm.ChatRequest{
			Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: systemPrompt},
				{Role: llm.RoleUser, Content: input},
			},
			Temperature: llm.Ptr(0.0),
		})
	}) {
		log.Printf("memextract: skipped, no background LLM slot within %v", bgStartTimeout)
		return 0
	}
	if llmErr != nil {
		return 0
	}

	candidates, ok := memextract.ParseResponse(text)
	if !ok {
		return 0
	}

	var ttl *time.Time
	if a.MemDefaultTTL > 0 {
		t := time.Now().Add(a.MemDefaultTTL)
		ttl = &t
	}

	storeCtx, cancelStore := context.WithTimeout(ctx, 60*time.Second)
	defer cancelStore()

	// Auto-extraction runs after the request, so open the user's database here.
	stored := 0
	_ = a.Storage.WithUser(storeCtx, userID, func(userDB *sql.DB) error {
		for _, c := range candidates {
			if !memextract.ShouldStore(c) {
				continue
			}
			fact := c.Content
			// Skip facts already covered (exact text, same slot key as an existing
			// memory, or verbatim content written via memory_write this turn).
			if a.Memory.IsRedundant(storeCtx, userDB, userID, fact, turnWritten) {
				continue
			}
			scope := c.Scope
			if scope == "shared" && !canShareMemory {
				scope = "user"
			}
			if _, err := a.Memory.Add(storeCtx, userDB, types.CreateMemoryRequest{
				Scope: scope, Content: fact, Source: "auto", ExpiresAt: ttl,
			}, userID); err != nil {
				log.Printf("memextract: store memory failed: %v", err)
			} else {
				stored++
			}
		}
		return nil
	})
	return stored
}
