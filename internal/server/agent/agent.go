// Package agent implements the agentic tool-calling loop: it composes a prompt
// from resolved skills, calls the LLM provider, dispatches tool calls, and loops
// until a final answer. Every trajectory event is emitted to the session log.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/ivoras/harlequin/internal/server/conversation"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Agent runs the tool-calling loop.
type Agent struct {
	Provider      llm.Provider
	Memory        *memory.Store
	Docs          *documents.Store
	Skills        *skills.Manager
	Runner        *jsrun.Runner
	Conversations *conversation.Store
	Session       *sessionlog.Logger

	MaxSteps      int
	AutoExtract   bool
	MemDefaultTTL time.Duration

	// RecordUsage, if set, is called with attributed token usage per completion.
	RecordUsage func(ctx context.Context, userID int64, conversationID *int64, provider, model string, u llm.Usage)
}

// EmitFunc receives streaming events for the client (SSE).
type EmitFunc func(types.StreamEvent)

// runContext carries per-request state.
type runContext struct {
	conversationID int64
	userID         int64
	username       string
	turn           int
	step           int
}

const systemPreamble = `You are Harlequin, a helpful AI assistant for an organisation.
You have access to tools: use them when helpful. You can search and write memory,
list and load skills (which contain instructions and resources), run JavaScript via
run_js, and search organisation documents. Prefer loading a relevant skill before
answering a specialised request. Be concise and accurate.`

// Run executes a full turn for the given user message, streaming events via emit.
func (a *Agent) Run(ctx context.Context, conversationID, userID int64, username, userContent string, emit EmitFunc) error {
	rc := &runContext{conversationID: conversationID, userID: userID, username: username, turn: 1}

	a.logEvent(ctx, rc, sessionlog.TypeSessionStart, map[string]any{
		"max_steps": a.MaxSteps,
		"provider":  a.Provider.Name(),
	})
	a.logEvent(ctx, rc, sessionlog.TypeUserMessage, map[string]any{"content": userContent})

	// Persist the user message.
	if _, err := a.Conversations.AddMessage(ctx, conversationID, llm.RoleUser, userContent, nil); err != nil {
		return err
	}

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
	history, err := a.Conversations.Messages(ctx, conversationID)
	if err != nil {
		return err
	}
	systemPrompt := a.composeSystemPrompt(ctx, rc)
	a.logEvent(ctx, rc, sessionlog.TypeSystemPrompt, map[string]any{"content": systemPrompt})

	msgs := []llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}}
	for _, m := range history {
		msgs = append(msgs, llm.Message{Role: m.Role, Content: m.Content, ToolCalls: toLLMToolCalls(m.ToolCalls)})
	}

	var finalText string
	for step := 1; step <= a.MaxSteps; step++ {
		rc.step = step
		if ctx.Err() != nil {
			return ctx.Err()
		}

		a.logEvent(ctx, rc, sessionlog.TypeLLMRequest, map[string]any{
			"messages": len(msgs), "tools": len(toolDefs), "tool_names": toolNames,
		})

		stream, err := a.Provider.Chat(ctx, llm.ChatRequest{Messages: msgs, Tools: toolDefs})
		if err != nil {
			emit(types.StreamEvent{Type: types.SSEError, Error: err.Error()})
			a.logEvent(ctx, rc, sessionlog.TypeError, map[string]any{"error": err.Error()})
			return err
		}

		var assistantText string
		var thinkingText string
		var toolCalls []llm.ToolCall
		var lastProvider, lastModel string
		for chunk := range stream {
			if chunk.Err != nil {
				emit(types.StreamEvent{Type: types.SSEError, Error: chunk.Err.Error()})
				a.logEvent(ctx, rc, sessionlog.TypeError, map[string]any{"error": chunk.Err.Error()})
				return chunk.Err
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
						a.RecordUsage(ctx, userID, &cid, chunk.Provider, chunk.Model, *chunk.Usage)
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
			if _, err := a.Conversations.AddMessage(ctx, conversationID, llm.RoleAssistant, assistantText, nil); err != nil {
				return err
			}
			break
		}

		// Record the assistant message that requested tools.
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: assistantText, ToolCalls: toolCalls})
		_, _ = a.Conversations.AddMessage(ctx, conversationID, llm.RoleAssistant, assistantText, fromLLMToolCalls(toolCalls))

		// Dispatch each tool call.
		for _, tc := range toolCalls {
			result := a.dispatch(ctx, rc, tools, tc, emit)
			msgs = append(msgs, llm.Message{
				Role:       llm.RoleTool,
				Content:    result,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
			_, _ = a.Conversations.AddMessage(ctx, conversationID, llm.RoleTool, result, nil)
		}
	}

	a.logEvent(ctx, rc, sessionlog.TypeSessionEnd, map[string]any{"status": "ok", "steps": rc.step})
	emit(types.StreamEvent{Type: types.SSEDone})

	// Background auto memory extraction.
	if a.AutoExtract {
		go a.extractMemories(context.Background(), userID, userContent, finalText)
	}
	return nil
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
	prompt := systemPreamble
	infos, err := a.Skills.List(ctx, rc.userID, rc.username)
	if err == nil && len(infos) > 0 {
		prompt += "\n\nAvailable skills (use load_skill to read full instructions):\n"
		for _, i := range infos {
			prompt += fmt.Sprintf("- %s: %s\n", i.Name, i.Description)
		}
	}
	return prompt
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
