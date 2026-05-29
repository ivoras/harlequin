package agent

import (
	"context"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/agent/memextract"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// extractMemories asks the LLM to distill durable facts and stores them as
// source='auto' user memories when confidence >= 7, deduped against existing content.
func (a *Agent) extractMemories(ctx context.Context, userID int64, userContent, assistantText string) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	convo := "User said: " + userContent + "\nAssistant said: " + assistantText
	stream, err := a.Provider.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: memextract.Prompt},
			{Role: llm.RoleUser, Content: convo},
		},
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

	for _, c := range candidates {
		if !memextract.ShouldStore(c) {
			continue
		}
		fact := c.Content
		if existing, err := a.Memory.Search(ctx, fact, userID, "user", 1); err == nil && len(existing) > 0 {
			if strings.EqualFold(strings.TrimSpace(existing[0].Content), fact) {
				continue
			}
		}
		_, _ = a.Memory.Add(ctx, types.CreateMemoryRequest{
			Scope: "user", Content: fact, Source: "auto", ExpiresAt: ttl,
		}, userID)
	}
}
