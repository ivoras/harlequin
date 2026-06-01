// Package agent implements the agentic tool-calling loop: it composes a prompt
// from resolved skills, calls the LLM provider, dispatches tool calls, and loops
// until a final answer. Every trajectory event is emitted to the session log.
package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/conversation"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/server/webfetch"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Agent runs the tool-calling loop.
type Agent struct {
	Provider      llm.Provider
	Storage       *storage.Manager
	Memory        *memory.Store
	Docs          *documents.Store
	Skills        *skills.Manager
	Runner        *jsrun.Runner
	Conversations *conversation.Store
	Session       *sessionlog.Logger
	WebFetcher    *webfetch.Client

	MaxSteps      int
	Temperature   float64
	AutoExtract   bool
	MemDefaultTTL time.Duration
	// WebFetchModel is the model used to analyse fetched web content (a small,
	// fast model). Empty uses the provider's default model.
	WebFetchModel string

	// RecordUsage, if set, is called with attributed token usage per completion.
	// userDB is the caller's open per-user database.
	RecordUsage func(ctx context.Context, userDB *sql.DB, userID int64, conversationID *int64, provider, model string, u llm.Usage)
	// ContextMax, if set, returns the model's max context window in tokens.
	ContextMax func(model string) int
}

// EmitFunc receives streaming events for the client (SSE).
type EmitFunc func(types.StreamEvent)

// runContext carries per-request state.
type runContext struct {
	conversationID int64
	userID         int64
	username       string
	canShareMemory bool // owner or admin: may create/delete shared memories
	userDB         *sql.DB    // the caller's open per-user database for this request
	hat            *types.Hat // the conversation's worn hat, or nil
	turn           int
	step           int
	emit           EmitFunc
	memWritten     []string // content stored/changed via memory_write or memory_change (auto-extract dedup)
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
func (a *Agent) Run(ctx context.Context, conversationID, userID int64, username, role, userContent string, emit EmitFunc) error {
	rc := &runContext{conversationID: conversationID, userID: userID, username: username, canShareMemory: types.IsElevated(role), turn: 1, emit: emit}

	var finalText string
	if err := a.Storage.WithUser(ctx, userID, func(userDB *sql.DB) error {
		rc.userDB = userDB
		ft, err := a.turn(ctx, rc, userContent)
		finalText = ft
		return err
	}); err != nil {
		return err
	}

	// Background auto memory extraction (opens its own per-user database).
	if a.AutoExtract {
		written := append([]string(nil), rc.memWritten...)
		go a.extractMemories(context.Background(), userID, userContent, finalText, written, rc.canShareMemory)
	}
	return nil
}

// turn runs the tool-calling loop for one user message and returns the final
// assistant text. rc.userDB must be an open per-user database.
func (a *Agent) turn(ctx context.Context, rc *runContext, userContent string) (string, error) {
	conversationID := rc.conversationID
	userID := rc.userID
	emit := rc.emit

	a.logEvent(ctx, rc, sessionlog.TypeSessionStart, map[string]any{
		"max_steps": a.MaxSteps,
		"provider":  a.Provider.Name(),
	})
	a.logEvent(ctx, rc, sessionlog.TypeUserMessage, map[string]any{"content": userContent})

	// Persist the user message.
	if _, err := a.Conversations.AddMessage(ctx, rc.userDB, conversationID, llm.RoleUser, userContent, nil); err != nil {
		return "", err
	}

	// Resolve the conversation's worn hat up front: it governs the system prompt
	// and which skills are visible (so it must be set before tools are built).
	a.loadHat(ctx, rc)

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
	a.logEvent(ctx, rc, sessionlog.TypeToolsAvailable, map[string]any{
		"count": len(toolCatalog), "tools": toolCatalog,
	})

	// Compose messages: system + history.
	history, err := a.Conversations.Messages(ctx, rc.userDB, conversationID)
	if err != nil {
		return "", err
	}
	systemPrompt := a.composeSystemPrompt(ctx, rc)
	a.logEvent(ctx, rc, sessionlog.TypeSystemPrompt, map[string]any{"content": systemPrompt})

	msgs := []llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}}
	for _, m := range history {
		msgs = append(msgs, llm.Message{Role: m.Role, Content: m.Content, ToolCalls: toLLMToolCalls(m.ToolCalls)})
	}

	var finalText string
	var turnModel string
	var turnContextTokens int
	var turnContextMax int
	for step := 1; step <= a.MaxSteps; step++ {
		rc.step = step
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		estimatedTokens := llm.EstimateMessagesTokens(msgs, toolDefs)

		a.logEvent(ctx, rc, sessionlog.TypeLLMRequest, map[string]any{
			"messages": len(msgs), "tools": len(toolDefs), "tool_names": toolNames,
			"temperature": a.Temperature, "estimated_prompt_tokens": estimatedTokens,
		})

		stream, err := a.Provider.Chat(ctx, llm.ChatRequest{
			Messages:    msgs,
			Tools:       toolDefs,
			Temperature: llm.Ptr(a.Temperature),
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
		for chunk := range stream {
			if chunk.Err != nil {
				emit(types.StreamEvent{Type: types.SSEError, Error: chunk.Err.Error()})
				a.logEvent(ctx, rc, sessionlog.TypeError, map[string]any{"error": chunk.Err.Error()})
				return "", chunk.Err
			}
			if chunk.ThinkingDelta != "" {
				thinkingText += chunk.ThinkingDelta
				emit(types.StreamEvent{Type: types.SSEThinking, Thinking: chunk.ThinkingDelta})
				if a.Session.LogTokens() {
					a.logEvent(ctx, rc, sessionlog.TypeThinkingDelta, map[string]any{"text": chunk.ThinkingDelta})
				}
			}
			if chunk.TextDelta != "" {
				assistantText += chunk.TextDelta
				emit(types.StreamEvent{Type: types.SSEToken, Text: chunk.TextDelta})
				if a.Session.LogTokens() {
					a.logEvent(ctx, rc, sessionlog.TypeLLMDelta, map[string]any{"text": chunk.TextDelta})
				}
			}
			if chunk.Done {
				toolCalls = chunk.ToolCalls
				lastProvider = chunk.Provider
				lastModel = chunk.Model
				turnModel = lastModel
				if chunk.Usage != nil && chunk.Usage.PromptTokens > 0 {
					turnContextTokens = chunk.Usage.PromptTokens
				} else {
					turnContextTokens = estimatedTokens
				}
				if a.ContextMax != nil {
					turnContextMax = a.ContextMax(turnModel)
				}
				if chunk.Usage != nil {
					a.logEvent(ctx, rc, sessionlog.TypeUsage, map[string]any{
						"provider":            chunk.Provider,
						"model":               chunk.Model,
						"prompt_tokens":       chunk.Usage.PromptTokens,
						"completion_tokens":   chunk.Usage.CompletionTokens,
						"total_tokens":        chunk.Usage.TotalTokens,
					})
					if a.RecordUsage != nil {
						cid := conversationID
						a.RecordUsage(ctx, rc.userDB, userID, &cid, chunk.Provider, chunk.Model, *chunk.Usage)
					}
				}
			}
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
			if _, err := a.Conversations.AddMessage(ctx, rc.userDB, conversationID, llm.RoleAssistant, assistantText, nil); err != nil {
				return "", err
			}
			break
		}

		// Record the assistant message that requested tools.
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: assistantText, ToolCalls: toolCalls})
		_, _ = a.Conversations.AddMessage(ctx, rc.userDB, conversationID, llm.RoleAssistant, assistantText, fromLLMToolCalls(toolCalls))

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
			_, _ = a.Conversations.AddMessage(ctx, rc.userDB, conversationID, llm.RoleTool, result, nil)
			if tc.Function.Name == "ask_user" {
				askedUser = true
			}
		}

		// ask_user yields control to the user: end the turn so they can reply
		// rather than letting the model answer its own question.
		if askedUser {
			finalText = assistantText
			break
		}
	}

	a.logEvent(ctx, rc, sessionlog.TypeSessionEnd, map[string]any{
		"status": "ok", "steps": rc.step,
		"model": turnModel, "context_tokens": turnContextTokens, "context_max": turnContextMax,
	})
	done := types.StreamEvent{Type: types.SSEDone, Model: turnModel, ContextTokens: turnContextTokens, ContextMax: turnContextMax}
	emit(done)

	return finalText, nil
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
	infos, err := a.Skills.EffectiveSkillInfos(ctx, rc.userDB, rc.userID, rc.username, rc.hat)
	if err == nil && len(infos) > 0 {
		prompt += "\n\nAvailable skills (use load_skill to read full instructions):\n"
		for _, i := range infos {
			prompt += fmt.Sprintf("- %s: %s\n", i.Name, i.Description)
		}
	}
	return prompt
}

// loadHat sets rc.hat from the conversation's worn hat (if any), reading the
// hat definition from the deployed hats directory.
func (a *Agent) loadHat(ctx context.Context, rc *runContext) {
	conv, err := a.Conversations.Get(ctx, rc.userDB, rc.conversationID, rc.userID)
	if err != nil || conv.Hat == nil || *conv.Hat == "" {
		return
	}
	if hat, err := a.Skills.GetHat(*conv.Hat); err == nil {
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
		ConversationID: rc.conversationID,
		UserID:         rc.userID,
		Turn:           rc.turn,
		Step:           rc.step,
		Type:           typ,
		Data:           data,
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
