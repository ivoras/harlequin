package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// toolHandler executes a tool call and returns a string result.
type toolHandler func(ctx context.Context, rc *runContext, args map[string]any) (string, error)

// toolEntry pairs a definition with its handler.
type toolEntry struct {
	def     llm.Tool
	handler toolHandler
}

// buildTools assembles the tool registry for a request: built-ins plus any
// skill-defined tools, resolved for the requesting user.
func (a *Agent) buildTools(ctx context.Context, rc *runContext) map[string]toolEntry {
	reg := map[string]toolEntry{}

	reg["memory_search"] = toolEntry{
		def: fnTool("memory_search", "Search the user's and shared memory and finds remembered facts, preferences, habits and information about the user and their environment. Each hit includes composite id (u.N/s.N) and slot_key when present — use those with memory_change or memory_delete.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []string{"query"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			q, _ := args["query"].(string)
			res, err := a.Memory.Search(ctx, rc.userDB, q, rc.userID, "", 6)
			if err != nil {
				return "", err
			}
			return renderResults(res), nil
		},
	}

	reg["memory_write"] = toolEntry{
		def: fnTool("memory_write", `Store a durable fact in memory.

scope "shared" (org-wide, owner/admin only): organisation identity and org-wide facts (company name, brand, domain, stack, policies, products); plus generic factual statements about the world outside the user's personal concerns (public definitions, standards, geography, science — objective facts not about this individual). Plain "The company name is …" → shared.

scope "user" (default): personal preferences and habits, private or sensitive information, facts about this individual only ("User prefers …", "I like …"). If unsure and you are not owner/admin, use user.

Only owner/admin may use shared. When you are owner/admin and the user states an org-wide fact, prefer shared over user.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{"type": "string"},
				"scope":   map[string]any{"type": "string", "enum": []string{"user", "shared"}},
			},
			"required": []string{"content"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			content, _ := args["content"].(string)
			scope, _ := args["scope"].(string)
			if scope == "" {
				scope = "user"
			}
			if scope == "shared" && !rc.canShareMemory {
				return "error: only owner or admin users can create shared memories; store this as a user-scoped memory instead, or ask an owner/admin.", nil
			}
			mem, hits, err := a.Memory.AddWithConflicts(ctx, rc.userDB, types.CreateMemoryRequest{Scope: scope, Content: content, Source: "tool"}, rc.userID)
			if err != nil {
				return "", err
			}
			rc.memWritten = append(rc.memWritten, content)
			if len(hits) == 0 {
				return fmt.Sprintf("Stored as memory %s.", mem.ID), nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "Stored as memory %s, but it conflicts with existing memories:\n", mem.ID)
			for _, h := range hits {
				fmt.Fprintf(&sb, "- %s [%s] %q (%s)\n", h.OtherID, h.Relationship, strings.TrimSpace(h.OtherContent), h.Reason)
			}
			sb.WriteString("Tell the user about this conflict and use ask_user to ask how to resolve it (e.g. update the old memory with memory_change, keep the new and delete the old, discard the new, or keep both).")
			return sb.String(), nil
		},
	}

	reg["memory_change"] = toolEntry{
		def: fnTool("memory_change", `Replace the content of an existing memory (same composite id; scope unchanged). Identify the memory by id (u.N or s.N) or slot_key (e.g. organisation.name) from memory_search or /memory — id is preferred if both are known. Use when the user corrects or updates a fact. Prefer this over memory_delete alone when a replacement is known.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":       map[string]any{"type": "string", "description": "Composite memory id, e.g. s.4"},
				"slot_key": map[string]any{"type": "string", "description": "Normalized slot key, e.g. organisation.name"},
				"content":  map[string]any{"type": "string"},
			},
			"required": []string{"content"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			id, errMsg := a.resolveMemoryRef(ctx, rc, args)
			if errMsg != "" {
				return errMsg, nil
			}
			content, _ := args["content"].(string)
			if strings.TrimSpace(content) == "" {
				return "error: content is required", nil
			}
			mem, hits, err := a.Memory.ChangeWithConflicts(ctx, rc.userDB, id, content, rc.userID, rc.canShareMemory)
			if err != nil {
				if errors.Is(err, memory.ErrNotFound) {
					return fmt.Sprintf("error: memory %s not found or not editable (shared memories require admin rights)", id), nil
				}
				return "", err
			}
			rc.memWritten = append(rc.memWritten, content)
			if len(hits) == 0 {
				return fmt.Sprintf("Updated memory %s.", mem.ID), nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "Updated memory %s, but the new text conflicts with other memories:\n", mem.ID)
			for _, h := range hits {
				fmt.Fprintf(&sb, "- %s [%s] %q (%s)\n", h.OtherID, h.Relationship, strings.TrimSpace(h.OtherContent), h.Reason)
			}
			sb.WriteString("Tell the user and use ask_user if they need to resolve further.")
			return sb.String(), nil
		},
	}

	reg["memory_delete"] = toolEntry{
		def: fnTool("memory_delete", `Delete a memory by id (u.N/s.N) or slot_key from memory_search or /memory. Use when discarding a fact with no replacement, or after memory_change/memory_write stored the replacement. Never delete alone when the user asked to update a value.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":       map[string]any{"type": "string", "description": "Composite memory id"},
				"slot_key": map[string]any{"type": "string", "description": "Normalized slot key"},
			},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			id, errMsg := a.resolveMemoryRef(ctx, rc, args)
			if errMsg != "" {
				return errMsg, nil
			}
			if err := a.Memory.Delete(ctx, rc.userDB, id, rc.userID, rc.canShareMemory); err != nil {
				if errors.Is(err, memory.ErrNotFound) {
					return fmt.Sprintf("error: memory %s not found or not deletable (shared memories require admin rights)", id), nil
				}
				return "", err
			}
			return fmt.Sprintf("Deleted memory %s.", id), nil
		},
	}

	reg["ask_user"] = toolEntry{
		def: fnTool("ask_user", "Ask the user a question and pause for their reply. Use when you need the user to decide how to proceed. The turn ends after this call; the user's next message is their answer, so do not assume or invent it.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string"},
				"options": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional list of suggested answers to present to the user.",
				},
			},
			"required": []string{"question"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			question, _ := args["question"].(string)
			question = strings.TrimSpace(question)
			if question == "" {
				return "error: question is required", nil
			}
			opts := toStringSlice(args["options"])
			if rc.emit != nil {
				rc.emit(types.StreamEvent{Type: types.SSEAskUser, Text: question, Options: opts})
			}
			return "Question presented to the user; the turn now ends. Wait for the user's next message — do not answer on their behalf.", nil
		},
	}

	reg["list_skills"] = toolEntry{
		def: fnTool("list_skills", "List available skills with their descriptions.", map[string]any{
			"type": "object", "properties": map[string]any{},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			infos, err := a.Skills.EffectiveSkillInfos(ctx, rc.userDB, rc.userID, rc.username, rc.hat)
			if err != nil {
				return "", err
			}
			var sb strings.Builder
			for _, i := range infos {
				fmt.Fprintf(&sb, "- %s: %s\n", i.Name, i.Description)
			}
			if sb.Len() == 0 {
				return "(no skills)", nil
			}
			return sb.String(), nil
		},
	}

	reg["load_skill"] = toolEntry{
		def: fnTool("load_skill", "Load the full instructions and resources of a skill by name.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
			"required": []string{"name"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			sk, err := a.Skills.ResolveEffective(ctx, rc.userDB, name, rc.userID, rc.username, rc.hat)
			if err != nil {
				return "", err
			}
			a.logEvent(ctx, rc, sessionlog.TypeSkillLoaded, map[string]any{
				"name": sk.Name, "source": sk.Source, "files": len(sk.Files),
			})
			var sb strings.Builder
			for rel, content := range sk.Files {
				fmt.Fprintf(&sb, "=== %s ===\n%s\n", rel, content)
			}
			return sb.String(), nil
		},
	}

	reg["run_js"] = toolEntry{
		def: fnTool("run_js", "Execute JavaScript in a sandbox and return its output; ES5 only (var, not let/const; no arrows, classes, or async). Use println() and print() to emit output.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code": map[string]any{"type": "string"},
			},
			"required": []string{"code"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			code, _ := args["code"].(string)
			res, err := a.Runner.Run(code, jsrun.RunContext{})
			if err != nil {
				return fmt.Sprintf("error: %v\noutput: %s", err, res.Output), nil
			}
			out := res.Output
			if res.Value != nil {
				if b, err := json.Marshal(res.Value); err == nil {
					out += "\nresult: " + string(b)
				}
			}
			return out, nil
		},
	}

	if a.WebFetcher != nil {
		reg["WebFetch"] = a.webFetchEntry()
	}

	if a.Docs != nil {
		reg["search_docs"] = toolEntry{
			def: fnTool("search_docs", "Search the organisation document corpus (RAG).", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				},
				"required": []string{"query"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				q, _ := args["query"].(string)
				res, err := a.Docs.Search(ctx, q, 6)
				if err != nil {
					return "", err
				}
				return renderResults(res), nil
			},
		}
	}

	// Skill-defined tools, namespaced <skill>.<tool>, for the visible (hat-aware) skills.
	infos, err := a.Skills.EffectiveSkillInfos(ctx, rc.userDB, rc.userID, rc.username, rc.hat)
	if err == nil {
		for _, info := range infos {
			sk, err := a.Skills.ResolveEffective(ctx, rc.userDB, info.Name, rc.userID, rc.username, rc.hat)
			if err != nil {
				continue
			}
			for _, td := range sk.Tools {
				a.registerSkillTool(reg, sk.Name, td)
			}
		}
	}

	// External MCP server tools (shared + user), namespaced mcp__<server>__<tool>.
	if a.MCP != nil {
		a.registerMCPTools(ctx, rc, reg)
	}

	return reg
}

func (a *Agent) registerSkillTool(reg map[string]toolEntry, skillName string, td skills.ToolDefinition) {
	full := skillName + "." + td.Name
	params := td.Parameters
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	run := td.Run
	reg[full] = toolEntry{
		def: fnTool(full, td.Description, params),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			res, err := a.Runner.Run(run, jsrun.RunContext{Globals: map[string]any{"args": args}})
			if err != nil {
				return fmt.Sprintf("error: %v\noutput: %s", err, res.Output), nil
			}
			out := res.Output
			if res.Value != nil {
				if b, err := json.Marshal(res.Value); err == nil {
					out += string(b)
				}
			}
			return out, nil
		},
	}
}

// toStringSlice coerces a decoded JSON value (typically []any of strings) into
// a []string, dropping non-string and empty entries.
func toStringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range raw {
		if s, ok := item.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func fnTool(name, desc string, params map[string]any) llm.Tool {
	return llm.Tool{
		Type: "function",
		Function: llm.FunctionDefinition{Name: name, Description: desc, Parameters: params},
	}
}

func renderResults(res []types.SearchResult) string {
	if len(res) == 0 {
		return "(no results)"
	}
	var sb strings.Builder
	sb.WriteString("Ranked memory results (prefer #1 when it directly answers the question; use id or slot_key with memory_change/memory_delete; do not invent facts not listed here):\n")
	for i, r := range res {
		fmt.Fprintf(&sb, "%d. [%s", i+1, r.ID)
		if k := strings.TrimSpace(r.SlotKey); k != "" {
			fmt.Fprintf(&sb, " {%s}", k)
		}
		fmt.Fprintf(&sb, "] %s\n", strings.TrimSpace(r.Content))
	}
	return sb.String()
}

// resolveMemoryRef returns the composite memory id from tool args (id or slot_key).
// Non-empty second return value is a tool-facing error string.
func (a *Agent) resolveMemoryRef(ctx context.Context, rc *runContext, args map[string]any) (string, string) {
	id, _ := args["id"].(string)
	slotKey, _ := args["slot_key"].(string)
	id = strings.TrimSpace(id)
	slotKey = strings.TrimSpace(slotKey)
	if id != "" {
		ref, err := a.Memory.ResolveRef(ctx, rc.userDB, id, rc.userID)
		if err != nil {
			return "", memoryRefError(err, "id", id)
		}
		return ref, ""
	}
	if slotKey != "" {
		ref, err := a.Memory.ResolveRef(ctx, rc.userDB, slotKey, rc.userID)
		if err != nil {
			return "", memoryRefError(err, "slot_key", slotKey)
		}
		return ref, ""
	}
	return "", "error: id or slot_key is required (from memory_search or /memory)"
}

func memoryRefError(err error, kind, value string) string {
	if errors.Is(err, memory.ErrNotFound) {
		return fmt.Sprintf("error: no memory found for %s %q", kind, value)
	}
	if errors.Is(err, memory.ErrAmbiguousRef) {
		return fmt.Sprintf("error: %s %q is ambiguous (%v); use id instead", kind, value, err)
	}
	return "error: " + err.Error()
}
