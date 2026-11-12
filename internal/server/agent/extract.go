package agent

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/agent/memextract"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// docMemoryInputCap bounds how much document text is fed to the memory distiller
// in one pass, so a large upload stays within the model's context and finishes
// within docMemoryTimeout. Extraction sees the leading portion: who the parties
// are and what the document establishes is usually stated up front, and the
// bridge wants nothing else (see memextract.DocumentPrompt).
const docMemoryInputCap = 8000

// Timeouts: sessional turns are small; a document prompt is much larger, so
// it needs longer (prefill of several thousand tokens on a local model).
const (
	sessMemoryTimeout = 60 * time.Second
	docMemoryTimeout  = 3 * time.Minute
	// storeTimeout bounds the whole store phase — embedding(s) plus the slot/
	// conflict-judge LLM calls — across all extracted candidates (not per item).
	storeTimeout = 120 * time.Second
)

// truncateFact shortens a fact for one-line failure logs.
func truncateFact(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// extractMemories asks the LLM to distill durable facts from a session turn and
// stores each in the scope the model filed it under. In a project session that
// includes the project's memory; a personal fact said there still goes to the
// user's own memory, not to one every project member reads.
func (a *Agent) extractMemories(ctx context.Context, projectID, userID int64, userContent, assistantText string, turnWritten []string, canShareMemory bool) {
	sess := "User said: " + userContent + "\nAssistant said: " + assistantText
	t := memTarget{userID: userID, projectID: projectID, canShare: canShareMemory, turnWritten: turnWritten}
	if n := a.distillAndStore(ctx, t, memextract.TurnPrompt(projectID > 0), sess, sessMemoryTimeout); n.total() > 0 {
		log.Printf("memextract: stored %s from session turn (user %d)", n, userID)
	}
}

// ExtractMemoriesFromDoc is the document→memory bridge: it distills the few
// lasting facts a newly added document reveals and stores them in the
// document's own scope (personal, shared or its project), each pointing back at
// the document. The model does not choose the scope: a client's contract
// uploaded to a project must not surface in organisation-wide memory.
// Best-effort; intended to run in a goroutine. It returns what was stored.
func (a *Agent) ExtractMemoriesFromDoc(ctx context.Context, userID int64, docScope string, projectID, docID int64, title, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "0 memories"
	}
	if len(text) > docMemoryInputCap {
		text = text[:docMemoryInputCap]
	}
	input := "Document"
	if title != "" {
		input += " (" + title + ")"
	}
	input += ":\n" + text
	// Documents about the same parties repeat who they are; exact-text matching
	// does not catch a restated address. Show the model what is already known
	// so each fact is stored once.
	if known := a.knownMemories(ctx, userID, docScope, projectID, title+"\n"+text); known != "" {
		input += "\n\nAlready in memory:\n" + known
	}
	t := memTarget{userID: userID, projectID: projectID, sourceDoc: docID, limit: memextract.MaxDocumentMemories}
	switch docScope {
	case documents.ScopeProject:
		t.force = memextract.ScopeProject
	case documents.ScopeShared:
		t.force, t.canShare = memextract.ScopeShared, true // the uploader was allowed to put the document there
	default:
		t.force = memextract.ScopeUser
	}
	// Not a session's spend, but still the uploader's (shows in their usage).
	ctx = llm.WithAttribution(ctx, llm.Attribution{UserID: userID})
	n := a.distillAndStore(ctx, t, memextract.DocumentPrompt, input, docMemoryTimeout)
	log.Printf("documents: memory bridge stored %s from %s document %d %q (uploaded by user %d)", n, docScope, docID, title, userID)
	return n.String()
}

// knownMemories lists the memories nearest to a document's opening (where the
// parties are named) among those visible from the document's scope, one per line.
func (a *Agent) knownMemories(ctx context.Context, userID int64, docScope string, projectID int64, text string) string {
	const queryRunes, hits = 1200, 20
	if r := []rune(text); len(r) > queryRunes {
		text = string(r[:queryRunes])
	}
	var res []types.SearchResult
	search := func(projDB *sql.DB) {
		_ = a.Storage.WithUser(ctx, userID, func(userDB *sql.DB) error {
			res, _ = a.Memory.SearchFused(ctx, userDB, projDB, text, userID, hits)
			return nil
		})
	}
	if docScope == documents.ScopeProject && projectID > 0 {
		_ = a.Storage.WithProject(ctx, projectID, func(projDB *sql.DB) error { search(projDB); return nil })
	} else {
		search(nil)
	}
	var sb strings.Builder
	for _, r := range res {
		sb.WriteString("- " + strings.TrimSpace(r.Content) + "\n")
	}
	return sb.String()
}

// memTarget says where extracted candidates may be stored.
type memTarget struct {
	userID, projectID int64
	canShare          bool     // may write shared memory (else shared is filed as user)
	turnWritten       []string // content memory_write already stored this turn
	force             string   // store everything in this scope, whatever the model said
	sourceDoc         int64    // the document the candidates come from (its local id in that scope)
	limit             int      // keep at most this many candidates, best first (0 = all)
}

// storeCounts tallies stored memories per target scope, for honest log lines
// ("for user N" used to read as if everything landed user-scoped).
type storeCounts struct {
	shared, user, project int
}

func (c storeCounts) total() int { return c.shared + c.user + c.project }

// String renders e.g. "2 memory(ies) (2 project)" / "3 memory(ies) (1 shared, 2 user)".
func (c storeCounts) String() string {
	var parts []string
	for _, p := range []struct {
		n    int
		name string
	}{{c.shared, "shared"}, {c.project, "project"}, {c.user, "user"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.name))
		}
	}
	if len(parts) == 0 {
		return "0 memories"
	}
	return fmt.Sprintf("%d memory(ies) (%s)", c.total(), strings.Join(parts, ", "))
}

// distillAndStore runs the memory-extraction LLM over input, then stores each
// accepted, non-redundant candidate. The whole distill+store sequence holds the
// background-LLM slot: storing also makes LLM calls (the conflict judge and
// slot canonicalization inside Memory.Add), so releasing the slot before
// storage would put judge completions back in parallel with the titler or a
// live turn. A live turn preempts the job; it restarts afterwards (the
// redundancy checks make the re-run skip anything already stored).
func (a *Agent) distillAndStore(ctx context.Context, t memTarget, systemPrompt, input string, timeout time.Duration) storeCounts {
	var stored storeCounts
	if !a.RunBackgroundLLM(ctx, func(jobCtx context.Context) {
		stored = a.distillAndStoreHoldingSlot(jobCtx, t, systemPrompt, input, timeout)
	}) {
		log.Printf("memextract: skipped, background LLM slot unavailable")
	}
	return stored
}

// distillAndStoreHoldingSlot is the body of distillAndStore; the caller holds
// the background-LLM slot.
func (a *Agent) distillAndStoreHoldingSlot(ctx context.Context, t memTarget, systemPrompt, input string, timeout time.Duration) storeCounts {
	// The extraction LLM call gets its own deadline. Storage (embedding + insert)
	// gets a separate one derived from the parent, so a slow extraction can't
	// starve the store step of time (which previously made every embed time out).
	llmCtx, cancelLLM := context.WithTimeout(ctx, timeout)
	text, _, err := a.completeOnce(llmCtx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: systemPrompt},
			{Role: llm.RoleUser, Content: input},
		},
		Temperature: llm.Ptr(0.0),
	})
	cancelLLM()
	if err != nil {
		return storeCounts{}
	}
	candidates, ok := memextract.ParseResponse(text)
	if !ok {
		return storeCounts{}
	}
	var keep []memextract.Candidate
	for _, c := range candidates {
		if memextract.ShouldStore(c) {
			keep = append(keep, c)
		}
	}
	if t.limit > 0 && len(keep) > t.limit {
		sort.SliceStable(keep, func(i, j int) bool { return keep[i].Confidence > keep[j].Confidence })
		keep = keep[:t.limit]
	}
	if len(keep) == 0 {
		return storeCounts{}
	}

	var ttl *time.Time
	if a.MemDefaultTTL > 0 {
		exp := time.Now().Add(a.MemDefaultTTL)
		ttl = &exp
	}
	storeCtx, cancelStore := context.WithTimeout(ctx, storeTimeout)
	defer cancelStore()

	var stored storeCounts
	var toProject []string
	// Extraction runs after the request, so open the databases here.
	_ = a.Storage.WithUser(storeCtx, t.userID, func(userDB *sql.DB) error {
		for _, c := range keep {
			scope := c.Scope
			if t.force != "" {
				scope = t.force
			}
			if scope == memextract.ScopeShared && !t.canShare {
				scope = memextract.ScopeUser
			}
			if scope == memextract.ScopeProject {
				if t.projectID > 0 {
					toProject = append(toProject, c.Content)
					continue
				}
				scope = memextract.ScopeUser // no project to file it under
			}
			// Skip facts already covered (exact text, same slot key as an existing
			// memory, or verbatim content written via memory_write this turn).
			if a.Memory.IsRedundant(storeCtx, userDB, t.userID, c.Content, t.turnWritten) {
				continue
			}
			if _, err := a.Memory.Add(storeCtx, userDB, types.CreateMemoryRequest{
				Scope: scope, Content: c.Content, Source: "auto", ExpiresAt: ttl, SourceDoc: t.sourceDoc,
			}, t.userID); err != nil {
				// The wrapped error names the failing step (e.g. "embed memory
				// content", "insert memory row", "embed slot vector").
				log.Printf("memextract: store memory failed (%q): %v", truncateFact(c.Content), err)
			} else if scope == memextract.ScopeShared {
				stored.shared++
			} else {
				stored.user++
			}
		}
		return nil
	})
	if len(toProject) > 0 {
		written := map[string]bool{}
		for _, w := range t.turnWritten {
			written[strings.ToLower(strings.TrimSpace(w))] = true
		}
		_ = a.Storage.WithProject(storeCtx, t.projectID, func(projDB *sql.DB) error {
			for _, fact := range toProject {
				// Project memory has no slots or conflict judge: the same text
				// already there (or just written by memory_write) is the duplicate.
				var dup int
				_ = projDB.QueryRowContext(storeCtx, `SELECT 1 FROM memories WHERE lower(trim(content)) = lower(trim(?)) LIMIT 1`, fact).Scan(&dup)
				if dup == 1 || written[strings.ToLower(strings.TrimSpace(fact))] {
					continue
				}
				if _, err := a.Memory.ProjectAddFrom(storeCtx, projDB, fact, "auto", t.sourceDoc); err != nil {
					log.Printf("memextract: store project memory failed (%q): %v", truncateFact(fact), err)
					return err
				}
				stored.project++
			}
			return nil
		})
	}
	return stored
}
