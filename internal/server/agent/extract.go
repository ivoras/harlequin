package agent

import (
	"context"
	"database/sql"
	"time"

	"github.com/ivoras/harlequin/internal/server/agent/memextract"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// extractMemories asks the LLM to distill durable facts and stores them when
// confidence >= 7, deduped against existing content. canShareMemory allows
// scope "shared" from extraction; otherwise shared candidates are stored as user.
func (a *Agent) extractMemories(ctx context.Context, userID int64, userContent, assistantText string, turnWritten []string, canShareMemory bool) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	convo := "User said: " + userContent + "\nAssistant said: " + assistantText
	stream, err := a.Provider.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: memextract.Prompt},
			{Role: llm.RoleUser, Content: convo},
		},
		Temperature: llm.Ptr(0.0),
	})
	if err != nil {
		return
	}
	var text string
	for chunk := range stream {
		if chunk.Err != nil {
			return
		}
		text += chunk.TextDelta
	}

	candidates, ok := memextract.ParseResponse(text)
	if !ok {
		return
	}

	var ttl *time.Time
	if a.MemDefaultTTL > 0 {
		t := time.Now().Add(a.MemDefaultTTL)
		ttl = &t
	}

	// Auto-extraction runs after the request, so open the user's database here.
	_ = a.Storage.WithUser(ctx, userID, func(userDB *sql.DB) error {
		for _, c := range candidates {
			if !memextract.ShouldStore(c) {
				continue
			}
			fact := c.Content
			// Skip facts already covered (exact text, same slot key as an existing
			// memory, or verbatim content written via memory_write this turn).
			if a.Memory.IsRedundant(ctx, userDB, userID, fact, turnWritten) {
				continue
			}
			scope := c.Scope
			if scope == "shared" && !canShareMemory {
				scope = "user"
			}
			_, _ = a.Memory.Add(ctx, userDB, types.CreateMemoryRequest{
				Scope: scope, Content: fact, Source: "auto", ExpiresAt: ttl,
			}, userID)
		}
		return nil
	})
}
