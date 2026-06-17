package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const (
	// autoTitleInterval is how often the background titler scans for work.
	autoTitleInterval = time.Minute
	// interfaceAliveWindow is how recently an interface must have made a request
	// to be considered live (and thus worth notifying about a new title).
	interfaceAliveWindow = 3 * time.Minute
	// autoTitleMaxWords caps the generated title length.
	autoTitleMaxWords = 6
	// autoTitleMsgChars / autoTitleTotalChars bound the transcript sent to the LLM
	// (keeps the prompt small for slow local models).
	autoTitleMsgChars   = 400
	autoTitleTotalChars = 2000
)

// RunAutoTitle runs the background session auto-titler until ctx is cancelled:
// once a minute, whenever the default LLM is free (no live turn), it gives a short
// title to any generically-named session that has a user message. Blocks; run in
// a goroutine.
func (a *Agent) RunAutoTitle(ctx context.Context, enabled bool) {
	if !enabled {
		return
	}
	t := time.NewTicker(autoTitleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.titlePass(ctx)
		}
	}
}

// llmFree reports whether no agent turn is currently using the LLM, so
// background jobs (titling, extraction — see RunBackgroundLLM) can borrow it
// without competing with a live turn.
func (a *Agent) llmFree() bool { return a.inFlight.Load() == 0 }

// titlePass titles eligible sessions for every user. The per-title LLM call is
// gated by RunBackgroundLLM; the llmFree checks here are just cheap early
// bails so the pass doesn't scan databases while a turn is running.
func (a *Agent) titlePass(ctx context.Context) {
	if !a.llmFree() {
		return
	}
	_ = a.Storage.EachUser(ctx, func(userID int64, udb *sql.DB) error {
		if !a.llmFree() {
			return context.Canceled // a turn started; resume next tick
		}
		sessions, err := a.Sessions.UntitledWithUserMessage(ctx, udb)
		if err != nil {
			return nil // skip this user on error
		}
		for _, c := range sessions {
			if !a.llmFree() {
				return context.Canceled
			}
			a.titleOne(ctx, udb, userID, c.ID, c.Interface)
		}
		return nil
	})
}

// titleOne summarizes one session into a title, persists it, and notifies
// the client to refresh its header.
func (a *Agent) titleOne(ctx context.Context, udb *sql.DB, userID, sessID int64, iface string) {
	msgs, err := a.Sessions.Messages(ctx, udb, sessID)
	if err != nil || len(msgs) == 0 {
		return
	}
	title, err := a.generateTitle(ctx, msgs)
	if err != nil || title == "" {
		return
	}
	if err := a.Sessions.SetTitle(ctx, udb, sessID, title); err != nil {
		log.Printf("auto-title: set title for sess %d (user %d): %v", sessID, userID, err)
		return
	}
	log.Printf("auto-title: sess %d (user %d) -> %q", sessID, userID, title)
	// Notify only the interface that owns this session, and only if it's live —
	// no point telling clients that aren't listening.
	if a.Notify != nil && a.Presence != nil && a.Presence.Alive(userID, iface, interfaceAliveWindow) {
		cid := sessID
		_, _ = a.Notify.Create(ctx, udb, types.Notification{
			Kind:      types.NotifyKindSessionTitle,
			Title:     title,
			SessionID: &cid,
			Interface: iface,
		})
	}
}

// generateTitle asks the default LLM for a terse title (<= autoTitleMaxWords).
func (a *Agent) generateTitle(ctx context.Context, msgs []types.Message) (string, error) {
	transcript := buildTranscript(msgs)
	if strings.TrimSpace(transcript) == "" {
		return "", nil
	}
	const sys = "You write a terse title for a chat session. Reply with ONLY the title: at most 6 words, no surrounding quotes, no trailing punctuation, no preamble."
	// The completion shares the background-LLM slot with memory extraction: one
	// background job at a time, started only while no live turn is on the model,
	// preempted (and retried) if a live turn begins mid-completion.
	var text string
	var err error
	if !a.RunBackgroundLLM(ctx, func(jobCtx context.Context) {
		text, _, err = a.completeOnce(jobCtx, llm.ChatRequest{
			Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: sys},
				{Role: llm.RoleUser, Content: "Session:\n" + transcript + "\n\nTitle:"},
			},
			Temperature: llm.Ptr(0.2),
		})
	}) {
		return "", errors.New("no background LLM slot")
	}
	if err != nil {
		return "", err
	}
	return cleanTitle(text), nil
}

// buildTranscript renders the user/assistant turns into a compact, bounded text.
func buildTranscript(msgs []types.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		if m.Role != llm.RoleUser && m.Role != llm.RoleAssistant {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		role := "User"
		if m.Role == llm.RoleAssistant {
			role = "Assistant"
		}
		fmt.Fprintf(&sb, "%s: %s\n", role, truncate(content, autoTitleMsgChars))
		if sb.Len() >= autoTitleTotalChars {
			break
		}
	}
	return sb.String()
}

// cleanTitle normalizes the model's reply into a short, plain title.
func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	// Drop a leading "Title:" the model may echo.
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] // first line only
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "Title:"))
	s = strings.Trim(s, "\"'`")
	if fields := strings.Fields(s); len(fields) > autoTitleMaxWords {
		s = strings.Join(fields[:autoTitleMaxWords], " ")
	}
	s = strings.TrimRight(s, ".,;:!—-– ")
	return strings.TrimSpace(s)
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
