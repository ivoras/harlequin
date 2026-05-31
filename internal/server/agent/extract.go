package agent

import (
	"context"
	"database/sql"
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
			// Skip facts already in memory — the user's own OR shared — so we do
			// not store a user-scoped duplicate of, e.g., a shared memory the
			// assistant just wrote this turn at the user's request.
			if existing, err := a.Memory.Search(ctx, userDB, fact, userID, "", 5); err == nil && containsEqualFold(existing, fact) {
				continue
			}
			_, _ = a.Memory.Add(ctx, userDB, types.CreateMemoryRequest{
				Scope: "user", Content: fact, Source: "auto", ExpiresAt: ttl,
			}, userID)
		}
		return nil
	})
}

// containsEqualFold reports whether any search result's content equals fact
// (case-insensitive, trimmed).
func containsEqualFold(results []types.SearchResult, fact string) bool {
	fact = strings.TrimSpace(fact)
	for _, r := range results {
		if strings.EqualFold(strings.TrimSpace(r.Content), fact) {
			return true
		}
	}
	return false
}
