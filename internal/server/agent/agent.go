// Package agent implements the agentic tool-calling loop: it composes a prompt
// from resolved skills, calls the LLM provider, dispatches tool calls, and loops
// until a final answer. Every trajectory event is emitted to the session log.
package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ivoras/harlequin/internal/server/audit"
	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/email"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/llm/loopdetect"
	"github.com/ivoras/harlequin/internal/server/mcp"
	"github.com/ivoras/harlequin/internal/server/mdpdf"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/notify"
	"github.com/ivoras/harlequin/internal/server/notifyx"
	"github.com/ivoras/harlequin/internal/server/pdfextract"
	"github.com/ivoras/harlequin/internal/server/presence"
	"github.com/ivoras/harlequin/internal/server/session"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/server/webfetch"
	"github.com/ivoras/harlequin/internal/server/websearch"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Agent runs the tool-calling loop.
type Agent struct {
	Provider llm.Provider
	Storage  *storage.Manager
	// Email, when SMTP is configured, enables the send_email tool.
	Email      *email.Sender
	emailLimit emailLimiter
	// Vision: the chat model accepts images, so attached image documents are
	// sent inline (see attachImages).
	Vision     bool
	Memory     *memory.Store
	Docs       *documents.Store
	Skills     *skills.Manager
	Runner     *jsrun.Runner
	Sessions   *session.Store
	Audit      *audit.Store // optional; nil-safe (stateless)
	Session    *sessionlog.Logger
	WebFetcher *webfetch.Client
	// MarkdownConverter (docling/xberg) backs the MarkdownFetch tool; nil disables it.
	MarkdownConverter MarkdownConverter
	// DocEvents, if set, is told about documents the agent creates, edits or deletes.
	DocEvents *docevents.Bus
	// WebSearch, if configured, provides the WebSearch tool (Brave Search API).
	WebSearch *websearch.Client
	MCP       *mcp.Manager
	// Cron, if set, lets the agent schedule/list/delete the user's cron jobs.
	Cron CronStore
	// Projects, if set, lets the search tools reach every project the user is
	// a member of (the all_projects option), not just the session's own.
	Projects ProjectLister
	// Notify, if set, lets background tasks (e.g. the auto-titler) raise
	// notifications for the client.
	Notify *notify.Store
	// NotifyDispatch, if set, lets the agent report which delivery channels
	// (in-app/email/telegram) are active for the user (the notify_channels tool).
	NotifyDispatch *notifyx.Dispatcher
	// Presence, if set, tells background tasks which (user, interface) pairs are
	// live, so they only notify connected interfaces.
	Presence *presence.Tracker

	// inFlight counts agent turns currently using the LLM (user chats and cron
	// skill runs). Background LLM jobs only start while it is zero, so they
	// never compete with a live turn. Pointer-receiver only (never copied).
	inFlight atomic.Int64
	// bgSlot is the single-slot semaphore that serializes background LLM jobs
	// (memory extraction, auto-titling); lazily created via bgOnce because the
	// Agent is constructed as a struct literal. bgCancel holds the in-flight
	// background job's cancel func so a starting turn can preempt it
	// immediately. See RunBackgroundLLM.
	bgOnce   sync.Once
	bgSlot   chan struct{}
	bgCancel atomic.Pointer[context.CancelFunc]

	MaxSteps      int
	Temperature   float64
	AutoExtract   bool
	MemDefaultTTL time.Duration
	// DataDir is the server data directory; per-user sandbox files live under
	// <DataDir>/users/<id>/.tmp and .storage.
	DataDir string
	// WebFetchModel is the model used to analyse fetched web content (a small,
	// fast model). Empty uses the provider's default model.
	WebFetchModel string
	// WebFetchTemperature is the sampling temperature for the content-analysis
	// call (low, for consistent extraction; separate from the chat Temperature).
	WebFetchTemperature float64
	// WebFetchTools offers the analysis model WebFetch/WebFetchDOM/calculator so
	// it can follow links and compute. Turn off for a small aux model, which gets
	// distracted into spurious tool calls; it then answers purely from the
	// fetched content (extraction-only).
	WebFetchTools bool
	// ReportTiming, when true, measures and reports per-turn model operation
	// timing (prompt processing, token generation, wall clock) to the client.
	ReportTiming bool

	// userDB is the caller's open per-user database.
	// ContextMax, if set, returns the model's max context window in tokens.
	ContextMax func(model string) int
	// PDFPages reads and copies pages of existing PDFs (pdf_pages, and exact
	// page numbers for check_text); nil when PDFium failed to start.
	PDFPages *pdfextract.Extractor
	// PDF typesets create_pdf's documents; nil when headless Chrome is not
	// installed (the tool then explains that instead of working).
	PDF *mdpdf.Renderer
	// CompactThreshold (tokens; 0 = off) and CompactKeep configure tier-1 context
	// compaction in the step loop (see compactHistory).
	CompactThreshold, CompactKeep int
}

// compactThreshold is the effective compaction trigger: the configured size,
// capped at 60% of the model's window when that is known.
func (a *Agent) compactThreshold(modelWindow int) int {
	thr := a.CompactThreshold
	if thr > 0 && modelWindow > 0 && thr > modelWindow*6/10 {
		thr = modelWindow * 6 / 10
	}
	return thr
}

// EmitFunc receives streaming events for the client (SSE).
type EmitFunc func(types.StreamEvent)

// runContext carries per-request state.
// ProjectLister yields the projects a user is a member of (satisfied by
// project.Store); the search tools use it for the all_projects option.
type ProjectLister interface {
	List(ctx context.Context, userID int64) ([]types.Project, error)
	// Create makes a project with createdBy as its first member (create_project).
	Create(ctx context.Context, name string, createdBy int64) (*types.Project, error)
}

type runContext struct {
	sessionID      int64
	userID         int64
	username       string
	canShareMemory bool    // owner or admin: may create/delete shared memories
	userDB         *sql.DB // the acting user's per-user database (user-scoped:
	// skills, MCP, cron, notify channels, usage, personal memory)
	// sessDB holds this session's sessions/messages rows: the project database for
	// a project session, otherwise == userDB.
	sessDB *sql.DB
	// projectID is non-zero for a project session; projectDB then holds the
	// project's data (sessions/messages/memory). nil/zero for a personal session.
	projectID int64
	projectDB *sql.DB
	// foreignProjDBs caches read-only handles to OTHER projects the user is a
	// member of, opened lazily when a p<id>.N reference is resolved and closed
	// when the turn ends (closeForeignDBs).
	foreignProjDBs map[int64]*sql.DB
	hat            *types.Hat // the session's worn hat, or nil
	// skillInfos is the hat-aware visible-skill catalogue, resolved once per
	// turn (after loadHat) and reused by the system prompt, the catalogue log,
	// and skill-tool registration.
	skillInfos []types.SkillInfo
	// mcpNeedAuth lists "scope/name" of enabled OAuth MCP servers skipped this
	// turn because the user hasn't signed in (set by registerMCPTools).
	mcpNeedAuth []string
	api         string // transport this session arrived over (e.g. "REST")
	iface       string // interface/medium this session uses (e.g. "TUI")
	turn        int
	step        int
	emit        EmitFunc
	memWritten  []string // content stored/changed via memory_write or memory_change (auto-extract dedup)
	// Memory-usefulness instrumentation (no learning yet — just measurement):
	// memRecalled is every composite id surfaced by memory_search this turn;
	// memUseful is the subset the model explicitly cited via memory_feedback (only
	// ids that were actually recalled); memUsefulCalled records whether the model
	// called the tool at all, and memUsefulInvalid counts cited ids that were
	// never recalled (hallucinated / stale).
	memRecalled      []string
	memUseful        []string
	memUsefulCalled  bool
	memUsefulInvalid int
}

// systemPromptFile is the deployed, JS-templated default system prompt
// (skills/system_prompt.md, synced into <data_dir>/skills/).
const systemPromptFile = "system_prompt.md"

// fallbackSystemPrompt is used only if the deployed system_prompt.md is missing
// or unreadable, so the server is never prompt-less.
const fallbackSystemPrompt = `You are Harlequin, a helpful AI assistant for an organisation. Use the available tools when helpful; prefer loading a relevant skill before answering a specialised request. Be concise and accurate.`

// Run executes a full turn for the given user message, streaming events via
// emit. It opens the caller's per-user database for the duration of the turn
// and closes it before any background work.
// Run executes a full turn. projectID is non-zero for a project session, in which
// case the session's messages live in the project database (and project members
// share the session) while user-scoped tools (skills/MCP/cron) still use the
// acting user's database.
func (a *Agent) Run(ctx context.Context, projectID, sessionID, userID int64, username, role, api, iface, userContent string, emit EmitFunc) error {
	if api == "" {
		api = types.APIREST
	}
	if iface == "" {
		iface = types.InterfaceTUI
	}
	// Every LLM call made for this turn (and its post-turn extraction) is
	// recorded against this session; see llm.Attribution.
	attr := llm.Attribution{UserID: userID, ProjectID: projectID, SessionID: sessionID}
	ctx = llm.WithAttribution(ctx, attr)
	rc := &runContext{sessionID: sessionID, userID: userID, username: username, canShareMemory: types.IsElevated(role), api: api, iface: iface, projectID: projectID, turn: 1, emit: emit}
	defer rc.closeForeignDBs() // cross-project handles opened for p<id>.N refs

	var finalText string
	run := func(userDB *sql.DB) error {
		rc.userDB = userDB
		rc.sessDB = userDB // overridden below for a project session
		if projectID > 0 {
			return a.Storage.WithProject(ctx, projectID, func(projDB *sql.DB) error {
				rc.sessDB = projDB
				rc.projectDB = projDB
				ft, err := a.turn(ctx, rc, userContent)
				finalText = ft
				a.recordTurnError(ctx, rc, err)
				return err
			})
		}
		ft, err := a.turn(ctx, rc, userContent)
		finalText = ft
		a.recordTurnError(ctx, rc, err)
		return err
	}
	if err := a.Storage.WithUser(ctx, userID, run); err != nil {
		return err
	}

	// Background auto memory extraction (opens its own databases).
	if a.AutoExtract {
		written := append([]string(nil), rc.memWritten...)
		go a.extractMemories(llm.WithAttribution(context.Background(), attr), projectID, userID, userContent, finalText, written, rc.canShareMemory)
	}
	return nil
}

// turn runs the tool-calling loop for one user message and returns the final
// assistant text. rc.userDB must be an open per-user database.
func (a *Agent) turn(ctx context.Context, rc *runContext, userContent string) (string, error) {
	// Mark the LLM busy for the whole turn so background jobs stay off it, and
	// kick any in-flight background completion off the model right away.
	a.inFlight.Add(1)
	defer a.inFlight.Add(-1)
	a.preemptBackgroundLLM()

	sessionID := rc.sessionID
	emit := rc.emit

	a.logEvent(ctx, rc, sessionlog.TypeSessionStart, map[string]any{
		"max_steps": a.MaxSteps,
		"provider":  a.Provider.Name(),
		"api":       rc.api,
		"interface": rc.iface,
	})
	a.logEvent(ctx, rc, sessionlog.TypeUserMessage, map[string]any{"content": userContent})

	// Persist the user message.
	if _, err := a.Sessions.AddMessage(ctx, rc.sessDB, sessionID, llm.RoleUser, userContent, nil); err != nil {
		return "", err
	}

	// Echo the prompt as the first event of the turn so a (re)connecting client
	// renders it from the stream rather than optimistically — the resume buffer is
	// then a complete, self-contained record of the in-flight turn.
	emit(types.StreamEvent{Type: types.SSEUserMessage, Text: userContent})

	// Resolve the session's worn hat up front: it governs the system prompt
	// and which skills are visible (so it must be set before tools are built).
	a.loadHat(ctx, rc)
	rc.skillInfos, _ = a.Skills.EffectiveSkillInfos(ctx, rc.userDB, rc.projectDB, rc.hat)

	tools := a.buildTools(ctx, rc)
	toolDefs := make([]llm.Tool, 0, len(tools))
	toolNames := make([]string, 0, len(tools))
	toolCatalog := make([]map[string]any, 0, len(tools))
	for name, t := range tools {
		toolDefs = append(toolDefs, t.def)
		toolNames = append(toolNames, name)
		toolCatalog = append(toolCatalog, map[string]any{
			"name":        name,
			"description": t.def.Function.Description,
		})
	}
	sort.Strings(toolNames)
	sort.Slice(toolCatalog, func(i, j int) bool {
		return toolCatalog[i]["name"].(string) < toolCatalog[j]["name"].(string)
	})
	// Sort the tool definitions sent to the model too: they are built by iterating
	// a map above, so their order is otherwise random per request. The chat
	// template renders tools into the prompt prefix, so a shuffled order changes
	// the prefix every turn and defeats llama.cpp's prompt-prefix cache (the whole
	// context gets reprocessed instead of reused). Deterministic order keeps the
	// prefix byte-stable across turns.
	sortToolDefs(toolDefs)
	a.logEvent(ctx, rc, sessionlog.TypeToolsAvailable, map[string]any{
		"count": len(toolCatalog), "tools": toolCatalog,
	})

	// Log the skill catalogue offered to the model this turn (the hat-aware
	// visible set), so "which skills did the agent see" is greppable rather than
	// buried in the system prompt text.
	if infos := rc.skillInfos; infos != nil {
		skillCatalog := make([]map[string]any, 0, len(infos))
		for _, i := range infos {
			skillCatalog = append(skillCatalog, map[string]any{
				"name": i.Name, "description": i.Description, "source": i.Source,
			})
		}
		a.logEvent(ctx, rc, sessionlog.TypeSkillsAvailable, map[string]any{
			"count": len(skillCatalog), "skills": skillCatalog,
		})
	}

	// Compose messages: system + history.
	history, err := a.Sessions.Messages(ctx, rc.sessDB, sessionID)
	if err != nil {
		return "", err
	}
	systemPrompt := a.composeSystemPrompt(ctx, rc)
	a.logEvent(ctx, rc, sessionlog.TypeSystemPrompt, map[string]any{"content": systemPrompt})

	msgs := []llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}}
	for _, m := range history {
		if m.Role == llm.RoleError {
			continue // stored failure notice, for display only
		}
		msgs = append(msgs, llm.Message{
			Role: m.Role, Content: m.Content, ToolCalls: toLLMToolCalls(m.ToolCalls),
			ToolCallID: m.ToolCallID, Name: m.Name,
		})
	}

	a.attachImages(ctx, rc, msgs)

	var finalText string
	var turnModel string
	var turnContextTokens int
	// Known before the first call (the default model's window), so compaction is
	// already capped to the window on step 1; refined from each response's model.
	var turnContextMax int
	if a.ContextMax != nil {
		turnContextMax = a.ContextMax("")
	}

	// Per-turn model operation timing, aggregated over LLM calls.
	turnStart := time.Now()
	// Fallback (wall-clock) accumulators, used when the provider reports no
	// server-side timing. Prompt tokens are cache-discounted so PP is not
	// inflated by KV-cached session history.
	var timePromptTokens, timeCompletionTokens int
	var timePrefill, timeDecode time.Duration
	// Server-reported timing accumulators (preferred; prompt_n excludes cache).
	var srvPromptN, srvPredN int
	var srvPromptMS, srvPredMS float64
	var haveSrvTimings bool

	loopRetries := 0
	streamRetries := 0 // consecutive transient mid-stream failures of the current step
	answered := false  // the loop ended with a reply (or an ask_user), not by running out of steps
	for step := 1; step <= a.MaxSteps; step++ {
		rc.step = step
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		// Tier-1 context compaction: old tool results are stubbed in the request
		// once it is over budget. msgs itself stays whole (as does the stored
		// session): the stubs are re-derived from it every step, which is what
		// keeps them identical from one step and turn to the next.
		reqMsgs, reqTools := msgs, toolDefs
		if thr := a.compactThreshold(turnContextMax); thr > 0 {
			var st compactStats
			if reqMsgs, st = compactHistory(msgs, toolDefs, thr, a.CompactKeep); st.Elided > 0 {
				log.Printf("agent: session %d step %d compacted context: %d old tool results elided (%d in all), ~%d -> ~%d tokens", sessionID, step, st.Elided, st.Total, st.TokensBefore, st.TokensAfter)
				a.logEvent(ctx, rc, sessionlog.TypeContextCompacted, map[string]any{
					"elided": st.Elided, "elided_total": st.Total, "chars_freed": st.CharsFreed,
					"tokens_before": st.TokensBefore, "tokens_after": st.TokensAfter, "threshold": thr,
				})
			}
		}

		// The last allowed step gets no tools and a nudge to answer: a model
		// stuck re-running tools must still reply rather than the turn silently
		// ending on a tool result. The nudge is not stored in the session.
		if step == a.MaxSteps && a.MaxSteps > 1 {
			reqTools = nil
			reqMsgs = append(append([]llm.Message(nil), reqMsgs...), llm.Message{Role: llm.RoleUser, Content: stepLimitNudge})
		}

		estimatedTokens := llm.EstimateMessagesTokens(reqMsgs, reqTools)

		a.logEvent(ctx, rc, sessionlog.TypeLLMRequest, map[string]any{
			"messages": len(reqMsgs), "tools": len(reqTools), "tool_names": toolNames,
			"temperature": a.Temperature, "estimated_prompt_tokens": estimatedTokens,
			"final_step": len(reqTools) == 0 && len(toolDefs) > 0,
		})

		reqStart := time.Now()
		// A per-call context lets the loop detector cancel just this LLM call.
		callCtx, cancelCall := context.WithCancel(ctx)
		defer cancelCall()
		stream, err := a.Provider.Chat(callCtx, llm.ChatRequest{
			Messages:    reqMsgs,
			Tools:       reqTools,
			Temperature: llm.Ptr(a.Temperature),
			UseFallback: streamRetries > maxStreamRetries,
		})
		if err != nil {
			emit(types.StreamEvent{Type: types.SSEError, Error: err.Error()})
			a.logEvent(ctx, rc, sessionlog.TypeError, map[string]any{"error": err.Error()})
			return "", err
		}

		var assistantText string
		var thinkingText string
		var toolCalls []llm.ToolCall
		var lastProvider, lastModel string
		var firstTokenAt, doneAt time.Time
		lastPPPct := -1 // prompt-progress throttle (per LLM call)
		var thinkLoop, textLoop loopdetect.Detector
		loopKind, loopEvidence := "", ""
		retryStep := false
		for chunk := range stream {
			if chunk.PromptProgress != nil {
				// Live prefill progress (llama.cpp). Throttle to ~every 5% so the
				// frequent (per-batch) events don't flood the stream. llama.cpp's
				// `processed` is an absolute position that already includes the cached
				// prefix, so subtract cache from both sides to measure real work 0→100%.
				pp := chunk.PromptProgress
				total := pp.Total - pp.Cache
				done := pp.Processed - pp.Cache
				if done < 0 {
					done = 0
				}
				if total > 0 {
					pct := done * 100 / total
					if pct >= lastPPPct+5 || done >= total {
						lastPPPct = pct
						emit(types.StreamEvent{Type: types.SSEPromptProgress, PromptProcessed: done, PromptTotal: total})
					}
				}
				continue
			}
			if (chunk.ThinkingDelta != "" || chunk.TextDelta != "") && firstTokenAt.IsZero() {
				firstTokenAt = time.Now()
			}
			if chunk.Err != nil {
				// Keep what was generated before the failure: it is otherwise lost,
				// and is what you need to see why a long call went wrong.
				a.logPartialResponse(ctx, rc, assistantText, thinkingText, chunk.Err)
				if ctx.Err() == nil && llm.IsTransient(chunk.Err) && streamRetries < a.maxStreamAttempts() {
					// The provider dropped the stream mid-answer. Discard the partial
					// step and run it again (the last attempt on the fallback model).
					cancelCall()
					go func() { // drain so the provider goroutine can finish
						for range stream {
						}
					}()
					wait := llm.RetryBackoff[min(streamRetries, len(llm.RetryBackoff)-1)]
					streamRetries++
					log.Printf("agent: LLM stream failed (%v) — retrying step %d in %s (attempt %d/%d)", chunk.Err, step, wait, streamRetries, a.maxStreamAttempts())
					a.logEvent(ctx, rc, sessionlog.TypeLLMRetry, map[string]any{
						"error": chunk.Err.Error(), "attempt": streamRetries, "wait_seconds": wait.Seconds(),
						"fallback_next": streamRetries > maxStreamRetries,
					})
					if !llm.SleepBackoff(ctx, wait) {
						return "", ctx.Err()
					}
					emit(types.StreamEvent{Type: types.SSEStepReset})
					retryStep = true
					break
				}
				emit(types.StreamEvent{Type: types.SSEError, Error: chunk.Err.Error()})
				a.logEvent(ctx, rc, sessionlog.TypeError, map[string]any{"error": chunk.Err.Error()})
				return "", chunk.Err
			}
			if chunk.ThinkingDelta != "" {
				thinkingText += chunk.ThinkingDelta
				if hit, ev := thinkLoop.Feed(chunk.ThinkingDelta); hit {
					loopKind, loopEvidence = "thinking_repeat", ev
				} else if len(thinkingText) > maxThinkingChars {
					loopKind, loopEvidence = "thinking_runaway", ""
				}
				emit(types.StreamEvent{Type: types.SSEThinking, Thinking: chunk.ThinkingDelta})
				if a.Session.LogTokens() {
					a.logEvent(ctx, rc, sessionlog.TypeThinkingDelta, map[string]any{"text": chunk.ThinkingDelta})
				}
			}
			if chunk.TextDelta != "" {
				if hit, ev := textLoop.Feed(chunk.TextDelta); hit && loopKind == "" {
					loopKind, loopEvidence = "content_repeat", ev
				}
				assistantText += chunk.TextDelta
				emit(types.StreamEvent{Type: types.SSEToken, Text: chunk.TextDelta})
				if a.Session.LogTokens() {
					a.logEvent(ctx, rc, sessionlog.TypeLLMDelta, map[string]any{"text": chunk.TextDelta})
				}
			}
			if loopKind != "" {
				break
			}
			if chunk.Done {
				doneAt = time.Now()
				toolCalls = chunk.ToolCalls
				lastProvider = chunk.Provider
				lastModel = chunk.Model
				turnModel = lastModel
				if chunk.Usage != nil && chunk.Usage.PromptTokens > 0 {
					turnContextTokens = chunk.Usage.PromptTokens
				} else {
					turnContextTokens = estimatedTokens
				}
				if chunk.Usage != nil {
					// Discount cached prefill: count only tokens actually processed.
					processed := chunk.Usage.PromptTokens - chunk.Usage.CachedPromptTokens()
					if processed < 0 {
						processed = 0
					}
					timePromptTokens += processed
					timeCompletionTokens += chunk.Usage.CompletionTokens
				}
				if chunk.Timings != nil {
					haveSrvTimings = true
					srvPromptN += chunk.Timings.PromptN
					srvPredN += chunk.Timings.PredictedN
					srvPromptMS += chunk.Timings.PromptMS
					srvPredMS += chunk.Timings.PredictedMS
				}
				if a.ContextMax != nil {
					turnContextMax = a.ContextMax(turnModel)
				}
				if chunk.Usage != nil {
					a.logEvent(ctx, rc, sessionlog.TypeUsage, map[string]any{
						"provider":          chunk.Provider,
						"model":             chunk.Model,
						"prompt_tokens":     chunk.Usage.PromptTokens,
						"completion_tokens": chunk.Usage.CompletionTokens,
						"total_tokens":      chunk.Usage.TotalTokens,
						"cached_tokens":     chunk.Usage.CachedPromptTokens(),
						"cost_usd":          chunk.Usage.Cost,
						"ttft_ms":           chunk.TTFTMS,
						"duration_ms":       time.Since(reqStart).Milliseconds(),
					})
				}
			}
		}

		if retryStep {
			step--
			continue
		}
		streamRetries = 0

		if loopKind != "" {
			cancelCall()
			go func() { // let the provider goroutine finish; nobody reads the stream now
				for range stream {
				}
			}()
			a.logPartialResponse(ctx, rc, assistantText, thinkingText, fmt.Errorf("stopped: %s", loopKind))
			a.logEvent(ctx, rc, sessionlog.TypeReasoningLoop, map[string]any{
				"kind": loopKind, "thinking_chars": len(thinkingText), "content_chars": len(assistantText),
				"evidence": truncateRunes(loopEvidence, 400), "seconds": time.Since(reqStart).Round(time.Second).Seconds(),
				"retry": loopRetries < maxLoopRetries,
			})
			log.Printf("agent: LLM call stopped after %s: %s (%d thinking chars)", time.Since(reqStart).Round(time.Second), loopKind, len(thinkingText))
			loopRetries++
			if loopRetries > maxLoopRetries {
				err := fmt.Errorf("the model got stuck repeating itself (%s) and was stopped; please try again or rephrase", loopKind)
				emit(types.StreamEvent{Type: types.SSEError, Error: err.Error()})
				a.logEvent(ctx, rc, sessionlog.TypeError, map[string]any{"error": err.Error()})
				return "", err
			}
			// Retry the step once, nudging the model out of the rut. The notice is
			// transient: it is not stored in the session.
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: loopNudge})
			emit(types.StreamEvent{Type: types.SSEStepReset})
			step--
			continue
		}

		// Accumulate model operation timing for this step: prefill = request start
		// to first token, decode = first token to completion.
		if !firstTokenAt.IsZero() {
			if doneAt.IsZero() {
				doneAt = time.Now()
			}
			timePrefill += firstTokenAt.Sub(reqStart)
			timeDecode += doneAt.Sub(firstTokenAt)
		}

		a.logEvent(ctx, rc, sessionlog.TypeLLMResponse, map[string]any{
			"provider":   lastProvider,
			"model":      lastModel,
			"content":    assistantText,
			"thinking":   thinkingText,
			"tool_calls": logToolCalls(toolCalls),
		})

		if len(toolCalls) == 0 {
			finalText = assistantText
			// An empty reply to the last-step nudge still counts as no answer.
			answered = step < a.MaxSteps || strings.TrimSpace(assistantText) != ""
			if _, err := a.Sessions.AddMessage(ctx, rc.sessDB, sessionID, llm.RoleAssistant, assistantText, nil); err != nil {
				return "", err
			}
			break
		}

		// Record the assistant message that requested tools.
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: assistantText, ToolCalls: toolCalls})
		_, _ = a.Sessions.AddMessage(ctx, rc.sessDB, sessionID, llm.RoleAssistant, assistantText, fromLLMToolCalls(toolCalls))

		// Dispatch each tool call.
		askedUser := false
		for _, tc := range toolCalls {
			result := a.dispatch(ctx, rc, tools, tc, emit)
			msgs = append(msgs, llm.Message{
				Role:       llm.RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
			_, _ = a.Sessions.AddMessageFull(ctx, rc.sessDB, sessionID, llm.RoleTool, result, nil, tc.ID, tc.Function.Name)
			if tc.Function.Name == "ask_user" {
				askedUser = true
			}
		}

		// Tell the model its remaining budget at 60% and 85% of the step limit,
		// on this step's last tool result (this turn's context only; the stored
		// message is unchanged), so it wraps up instead of exploring until cut off.
		if note := stepBudgetNote(step, a.MaxSteps); note != "" && len(toolCalls) > 0 {
			msgs[len(msgs)-1].Content += note
		}

		// ask_user yields control to the user: end the turn so they can reply
		// rather than letting the model answer its own question.
		if askedUser {
			finalText = assistantText
			answered = true
			break
		}
	}

	if !answered && ctx.Err() == nil {
		msg := fmt.Sprintf("The assistant used all %d steps of this turn without writing an answer (it kept calling tools). Ask again more specifically, or ask it to answer from what it has found so far.", a.MaxSteps)
		emit(types.StreamEvent{Type: types.SSEError, Error: msg})
		a.logEvent(ctx, rc, sessionlog.TypeError, map[string]any{"error": msg, "reason": "max_steps"})
		return "", errors.New(msg)
	}

	var timing *types.TurnTiming
	if haveSrvTimings {
		timing = timingFromServer(srvPromptN, srvPredN, srvPromptMS, srvPredMS, time.Since(turnStart))
	} else {
		timing = buildTurnTiming(timePromptTokens, timeCompletionTokens, timePrefill, timeDecode, time.Since(turnStart))
	}

	sessionEnd := map[string]any{
		"status": "ok", "steps": rc.step,
		"model": turnModel, "context_tokens": turnContextTokens, "context_max": turnContextMax,
	}
	if timing != nil {
		sessionEnd["timing"] = timing
	}
	a.logEvent(ctx, rc, sessionlog.TypeSessionEnd, sessionEnd)

	// Memory-usefulness instrumentation: record what was recalled vs. what the
	// model cited as useful, so we can measure compliance before any learning is
	// driven from it. Only logged when memory was actually searched this turn.
	if len(rc.memRecalled) > 0 {
		a.logEvent(ctx, rc, sessionlog.TypeMemoryFeedback, map[string]any{
			"recalled":       rc.memRecalled,
			"recalled_count": len(rc.memRecalled),
			"useful_cited":   rc.memUseful,
			"useful_count":   len(rc.memUseful),
			"tool_called":    rc.memUsefulCalled,
			"invalid_cited":  rc.memUsefulInvalid,
		})
	}

	done := types.StreamEvent{Type: types.SSEDone, Model: turnModel, ContextTokens: turnContextTokens, ContextMax: turnContextMax}
	if a.ReportTiming {
		done.Timing = timing
	}
	emit(done)

	return finalText, nil
}

// timingFromServer builds per-turn timing from server-reported llama.cpp timings
// aggregated over the turn's calls. This is accurate under KV caching because
// prompt_n counts only the tokens actually evaluated (cache hits excluded).
func timingFromServer(promptN, predN int, promptMS, predMS float64, total time.Duration) *types.TurnTiming {
	if total <= 0 {
		return nil
	}
	t := &types.TurnTiming{
		PromptTokens:     promptN,
		CompletionTokens: predN,
		PrefillMS:        int64(promptMS),
		DecodeMS:         int64(predMS),
		TotalMS:          total.Milliseconds(),
	}
	if promptMS > 0 && promptN > 0 {
		t.PPRate = float64(promptN) / (promptMS / 1000)
	}
	if predMS > 0 && predN > 0 {
		t.TGRate = float64(predN) / (predMS / 1000)
	}
	return t
}

// buildTurnTiming assembles per-turn timing with PP/TG rates (tokens/sec) from
// wall-clock measurements (fallback when the provider reports no server timing).
// Returns nil when nothing measurable happened.
func buildTurnTiming(promptTokens, completionTokens int, prefill, decode, total time.Duration) *types.TurnTiming {
	if total <= 0 {
		return nil
	}
	t := &types.TurnTiming{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		PrefillMS:        prefill.Milliseconds(),
		DecodeMS:         decode.Milliseconds(),
		TotalMS:          total.Milliseconds(),
	}
	if prefill > 0 && promptTokens > 0 {
		t.PPRate = float64(promptTokens) / prefill.Seconds()
	}
	if decode > 0 && completionTokens > 0 {
		t.TGRate = float64(completionTokens) / decode.Seconds()
	}
	return t
}

// dispatch runs one tool call, emitting tool_call/tool_result events and logging.
func (a *Agent) dispatch(ctx context.Context, rc *runContext, tools map[string]toolEntry, tc llm.ToolCall, emit EmitFunc) string {
	name := tc.Function.Name
	args := parseToolArgs(tc.Function.Arguments)

	emit(types.StreamEvent{Type: types.SSEToolCall, ToolName: name, ToolArgs: tc.Function.Arguments})
	a.logEvent(ctx, rc, sessionlog.TypeToolCall, map[string]any{
		"id":       tc.ID,
		"name":     name,
		"args":     args,
		"args_raw": tc.Function.Arguments,
	})

	start := time.Now()
	entry, ok := tools[name]
	if !ok {
		msg := fmt.Sprintf("error: unknown tool %q", name)
		dur := time.Since(start)
		emit(types.StreamEvent{Type: types.SSEToolResult, ToolName: name, Output: msg, DurationMS: dur.Milliseconds()})
		a.logToolResult(ctx, rc, tc.ID, name, msg, dur, true, nil)
		return msg
	}

	out, err := entry.handler(ctx, rc, args)
	okResult := err == nil
	if err != nil {
		out = "error: " + err.Error()
	}
	dur := time.Since(start)
	emit(types.StreamEvent{Type: types.SSEToolResult, ToolName: name, Output: out, DurationMS: dur.Milliseconds()})
	a.logToolResult(ctx, rc, tc.ID, name, out, dur, okResult, err)
	return out
}

func parseToolArgs(raw string) map[string]any {
	var args map[string]any
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &args)
	}
	if args == nil {
		args = map[string]any{}
	}
	return args
}

func (a *Agent) logToolResult(ctx context.Context, rc *runContext, id, name, output string, dur time.Duration, ok bool, err error) {
	data := map[string]any{
		"id":           id,
		"name":         name,
		"ok":           ok,
		"output":       output,
		"output_bytes": len(output),
		"duration_ms":  dur.Milliseconds(),
		"duration_ns":  dur.Nanoseconds(),
	}
	if err != nil {
		data["error"] = err.Error()
	}
	a.logEvent(ctx, rc, sessionlog.TypeToolResult, data)
}

func logToolCalls(calls []llm.ToolCall) []map[string]any {
	if len(calls) == 0 {
		return nil
	}
	out := make([]map[string]any, len(calls))
	for i, tc := range calls {
		out[i] = map[string]any{
			"id":       tc.ID,
			"name":     tc.Function.Name,
			"args":     parseToolArgs(tc.Function.Arguments),
			"args_raw": tc.Function.Arguments,
		}
	}
	return out
}

// composeSystemPrompt builds the system prompt including the skill catalogue.
func (a *Agent) composeSystemPrompt(ctx context.Context, rc *runContext) string {
	prompt := a.basePrompt(rc)
	if rc.hat != nil {
		prompt += fmt.Sprintf("\n\nYou are wearing the %q hat.", rc.hat.Name)
	}
	if len(rc.skillInfos) > 0 {
		prompt += "\n\nAvailable skills (use load_skill to read full instructions):\n"
		for _, i := range rc.skillInfos {
			prompt += fmt.Sprintf("- %s: %s\n", i.Name, i.Description)
		}
	}
	if len(rc.mcpNeedAuth) > 0 {
		prompt += "\n\n" + mcpNeedAuthNote(rc.mcpNeedAuth)
	}
	return prompt
}

// loadHat sets rc.hat from the session's worn hat (if any), reading the
// hat definition from the shared database.
func (a *Agent) loadHat(ctx context.Context, rc *runContext) {
	sess, err := a.Sessions.Get(ctx, rc.sessDB, rc.sessionID, rc.userID)
	if err != nil || sess.Hat == nil || *sess.Hat == "" {
		return
	}
	if hat, err := a.Skills.GetHat(ctx, *sess.Hat); err == nil {
		rc.hat = hat
	}
}

// basePrompt returns the rendered base system prompt: the worn hat's prompt when
// it defines one, otherwise the deployed system_prompt.md (falling back to a
// built-in default if that file is missing).
func (a *Agent) basePrompt(rc *runContext) string {
	if rc.hat != nil && strings.TrimSpace(rc.hat.SystemPrompt) != "" {
		if out, err := a.Skills.RenderText(rc.hat.SystemPrompt, rc.userID, rc.username); err == nil && strings.TrimSpace(out) != "" {
			return out
		}
	}
	if out, err := a.Skills.RenderFile(systemPromptFile, rc.userID, rc.username); err == nil && strings.TrimSpace(out) != "" {
		return out
	}
	return fallbackSystemPrompt
}

func (a *Agent) logEvent(ctx context.Context, rc *runContext, typ string, data map[string]any) {
	if a.Session == nil {
		return
	}
	a.Session.Log(ctx, sessionlog.Event{
		SessionID: rc.sessionID,
		UserID:    rc.userID,
		Turn:      rc.turn,
		Step:      rc.step,
		Type:      typ,
		Data:      data,
	})
}

func toLLMToolCalls(tcs []types.ToolCall) []llm.ToolCall {
	if len(tcs) == 0 {
		return nil
	}
	out := make([]llm.ToolCall, len(tcs))
	for i, t := range tcs {
		out[i] = llm.ToolCall{ID: t.ID, Type: "function", Function: llm.FunctionCall{Name: t.Name, Arguments: t.Arguments}}
	}
	return out
}

func fromLLMToolCalls(tcs []llm.ToolCall) []types.ToolCall {
	if len(tcs) == 0 {
		return nil
	}
	out := make([]types.ToolCall, len(tcs))
	for i, t := range tcs {
		out[i] = types.ToolCall{ID: t.ID, Name: t.Function.Name, Arguments: t.Function.Arguments}
	}
	return out
}

const (
	// maxThinkingChars aborts a single LLM call whose reasoning alone exceeds
	// this (~75k tokens): far beyond any legitimate step, and the signature of a
	// runaway that does not repeat verbatim, so the loop detector cannot see it.
	maxThinkingChars = 300_000
	// maxLoopRetries is how many times a turn re-asks after stopping a looping call.
	maxLoopRetries = 1
	// maxStreamRetries is how many times a step whose stream died mid-answer is
	// re-run on the primary model before one last attempt on the fallback model.
	maxStreamRetries = 2
	loopNudge        = "[Harlequin notice] Your previous attempt was stopped because your reasoning got stuck repeating itself. Do not continue that line of reasoning. Pick the simplest reasonable approach and act now: call a tool, or answer. If you need a decision from the user, use ask_user."
)

// logPartialResponse records an LLM call that did not complete (error or
// cancelled): the text generated so far, with the thinking cut to its tail so a
// runaway cannot bloat the log.
func (a *Agent) logPartialResponse(ctx context.Context, rc *runContext, content, thinking string, cause error) {
	const keep = 20000
	tail := thinking
	if len(tail) > keep {
		tail = "…" + tail[len(tail)-keep:]
	}
	a.logEvent(ctx, rc, sessionlog.TypeLLMResponse, map[string]any{
		"partial": true, "error": cause.Error(),
		"content": content, "thinking_chars": len(thinking), "thinking_tail": tail,
	})
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// maxStreamAttempts is the retry budget for a mid-stream failure: the primary
// retries, plus one attempt on the fallback model when one is configured.
func (a *Agent) maxStreamAttempts() int {
	if fb, ok := a.Provider.(interface{ HasFallback() bool }); ok && fb.HasFallback() {
		return maxStreamRetries + 1
	}
	return maxStreamRetries
}

// recordTurnError stores a failed turn's error in the session so every client
// shows why the turn ended, also after a reload. It is never sent to a model.
func (a *Agent) recordTurnError(ctx context.Context, rc *runContext, turnErr error) {
	if turnErr == nil || ctx.Err() != nil || rc.sessDB == nil {
		return
	}
	if _, err := a.Sessions.AddMessage(context.WithoutCancel(ctx), rc.sessDB, rc.sessionID, llm.RoleError, turnErr.Error(), nil); err != nil {
		log.Printf("agent: store turn error for session %d: %v", rc.sessionID, err)
	}
}

// stepLimitNudge is sent (unsaved) with the last allowed step of a turn.
const stepLimitNudge = "You have reached the step limit for this turn, so tools are no longer available. Answer my question now, in my language, using only what you have already found above. If that is not enough for a complete answer, give what you have and say clearly what is missing."

// stepBudgetNote returns the reminder appended after the step that crosses
// 60% / 85% of maxSteps ("" otherwise, and for short limits where it's noise).
func stepBudgetNote(step, maxSteps int) string {
	if maxSteps < 10 {
		return ""
	}
	left := maxSteps - step
	switch step {
	case (maxSteps*60 + 99) / 100:
		return fmt.Sprintf("\n\n[Step budget: %d of %d steps left in this turn. If you already have enough to answer, answer now instead of calling more tools.]", left, maxSteps)
	case (maxSteps*85 + 99) / 100:
		return fmt.Sprintf("\n\n[Step budget: only %d of %d steps left in this turn. Stop gathering and answer with what you have; say what is missing if it isn't complete.]", left, maxSteps)
	}
	return ""
}
