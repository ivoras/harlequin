package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

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

Only owner/admin may use shared. When you are owner/admin and the user states an org-wide fact, prefer shared over user.

Optionally pass slot_key to file the fact under an exact attribute key (e.g. "user.preferred_currency"); the content is then stored as that slot's value verbatim and no conflict check runs. Omit slot_key for normal free-text facts.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content":  map[string]any{"type": "string"},
				"scope":    map[string]any{"type": "string", "enum": []string{"user", "shared"}},
				"slot_key": map[string]any{"type": "string", "description": "Optional exact slot key, e.g. user.name; content becomes its value."},
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
			// Explicit slot: store the fact under the exact (key, value) slot,
			// skipping LLM slot extraction and conflict detection. Idempotent —
			// writing the same key again updates the one memory rather than creating
			// duplicates, so a retry is harmless.
			if slotKey, _ := args["slot_key"].(string); strings.TrimSpace(slotKey) != "" {
				key := strings.TrimSpace(slotKey)
				id, created, err := a.Memory.WriteSlot(ctx, rc.userDB, scope, key, content, rc.userID, rc.canShareMemory)
				if err != nil {
					return "", err
				}
				rc.memWritten = append(rc.memWritten, content)
				if created {
					return fmt.Sprintf("Stored as memory %s under slot %s. Done — do not write it again.", id, key), nil
				}
				return fmt.Sprintf("Slot %s already existed; updated memory %s to this value (no duplicate created). Done — do not write it again.", key, id), nil
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
				sb.WriteString(conflictLine(h) + "\n")
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
				sb.WriteString(conflictLine(h) + "\n")
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
		def: fnTool("run_js", `Execute JavaScript in a sandbox and return its output; ES5 only (var, not let/const; no arrows, classes, async, or template literals). Emit output with println()/print().
Available helpers: fetch(url) -> {status, body, finalUrl, contentType}; dom.parse(html) -> handle, then dom.query(handle, cssSelector), dom.grep(handle, text), dom.json(handle); per-user file stores tmp.* and storage.* (read/write/list/remove/exists); load(uri)/include(uri) for skill://<skill>/<path>, storage://<path>, tmp://<path> scripts.
Pass code inline, OR set script=<uri> to run a saved script instead. An optional args object is exposed to the script as the global 'args'.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code":   map[string]any{"type": "string", "description": "Inline JS to run (ES5)."},
				"script": map[string]any{"type": "string", "description": "URI of a saved script to run instead of code: skill://<skill>/<path>, storage://<path>, or tmp://<path>."},
				"args":   map[string]any{"type": "object", "description": "Optional object exposed to the script as the global 'args'."},
			},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			code, _ := args["code"].(string)
			script, _ := args["script"].(string)
			rcx := a.jsRunContext(ctx, rc)
			if jsArgs, ok := args["args"].(map[string]any); ok {
				rcx.Globals = map[string]any{"args": jsArgs}
			}
			if s := strings.TrimSpace(script); s != "" {
				src, err := rcx.Resolve(s)
				if err != nil {
					return fmt.Sprintf("error: %v", err), nil
				}
				code = src
			}
			if strings.TrimSpace(code) == "" {
				return "error: provide either code or script", nil
			}
			start := time.Now()
			res, err := a.Runner.Run(code, rcx)
			if err != nil {
				if errors.Is(err, jsrun.ErrTimeout) {
					log.Printf("run_js: execution timed out after %dms (%d bytes of code): %s",
						time.Since(start).Milliseconds(), len(code), truncateArgs(code, 200))
				}
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

	reg["calculator"] = a.calculatorEntry()

	if a.WebFetcher != nil {
		reg["WebFetch"] = a.webFetchEntry()
		reg["WebFetchDOM"] = a.webFetchDOMEntry()
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

	if a.Cron != nil {
		reg["cron_create"] = toolEntry{
			def: fnTool("cron_create", `Schedule a recurring job for the user.
kind "js": run a JavaScript script with NO AI each time — best for cheap periodic checks like watching a website for changes. target is a script URI (skill://<skill>/<path>, storage://<path>, tmp://<path>) or inline ES5 code; input is a JSON object exposed to the script as the global 'args'.
kind "skill": run an agent turn — target is an optional skill name to use, prompt is the message.
spec is a cron schedule: 5-field "min hour dom mon dow", a @descriptor (@hourly, @daily), or "@every 30m".
Example (watch a saved web-extractor check every 30 min): cron_create(name="fzoeu", spec="@every 30m", kind="js", target="skill://web-extractor/lib/check.js", input="{\"name\":\"fzoeu\"}").`, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":   map[string]any{"type": "string"},
					"spec":   map[string]any{"type": "string", "description": "Cron schedule (5-field, @descriptor, or @every <dur>)"},
					"kind":   map[string]any{"type": "string", "enum": []string{"js", "skill"}},
					"target": map[string]any{"type": "string", "description": "js: script URI or inline code; skill: skill name"},
					"prompt": map[string]any{"type": "string", "description": "skill: message to send to the agent"},
					"input":  map[string]any{"type": "string", "description": "JSON object of inputs (js: exposed as args)"},
				},
				"required": []string{"name", "spec", "kind", "target"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				req := types.CreateCronJobRequest{
					Name:   argString(args, "name"),
					Spec:   argString(args, "spec"),
					Kind:   argString(args, "kind"),
					Target: argString(args, "target"),
					Prompt: argString(args, "prompt"),
					Input:  argString(args, "input"),
				}
				job, err := a.Cron.Create(ctx, rc.userDB, req)
				if err != nil {
					return "error: " + err.Error(), nil
				}
				next := "unscheduled"
				if job.NextRunAt != nil {
					next = job.NextRunAt.Format("2006-01-02 15:04")
				}
				return fmt.Sprintf("Created cron job #%d %q (%s, %s); next run %s.", job.ID, job.Name, job.Kind, job.Spec, next), nil
			},
		}

		reg["cron_list"] = toolEntry{
			def: fnTool("cron_list", "List the user's scheduled cron jobs.", map[string]any{
				"type": "object", "properties": map[string]any{},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				jobs, err := a.Cron.List(ctx, rc.userDB)
				if err != nil {
					return "error: " + err.Error(), nil
				}
				if len(jobs) == 0 {
					return "(no cron jobs)", nil
				}
				var sb strings.Builder
				for _, j := range jobs {
					state := "enabled"
					if !j.Enabled {
						state = "disabled"
					}
					fmt.Fprintf(&sb, "#%d %s [%s, %s, %s] target=%s", j.ID, j.Name, j.Kind, j.Spec, state, j.Target)
					if j.LastStatus != "" {
						fmt.Fprintf(&sb, " last=%s", j.LastStatus)
					}
					sb.WriteString("\n")
				}
				return strings.TrimRight(sb.String(), "\n"), nil
			},
		}

		reg["cron_delete"] = toolEntry{
			def: fnTool("cron_delete", "Delete one of the user's cron jobs by id.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer"},
				},
				"required": []string{"id"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				id := int64(argInt(args, "id", 0))
				if id <= 0 {
					return "error: a positive id is required", nil
				}
				if err := a.Cron.Delete(ctx, rc.userDB, id); err != nil {
					return "error: " + err.Error(), nil
				}
				return fmt.Sprintf("Deleted cron job #%d.", id), nil
			},
		}

		reg["cron_update"] = toolEntry{
			def: fnTool("cron_update", "Edit an existing cron job by id: change its schedule/target/etc., or enable/disable it. Only the fields you pass are changed.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":      map[string]any{"type": "integer"},
					"name":    map[string]any{"type": "string"},
					"spec":    map[string]any{"type": "string", "description": "New cron schedule"},
					"kind":    map[string]any{"type": "string", "enum": []string{"js", "skill"}},
					"target":  map[string]any{"type": "string"},
					"prompt":  map[string]any{"type": "string"},
					"input":   map[string]any{"type": "string", "description": "JSON object of inputs"},
					"enabled": map[string]any{"type": "boolean", "description": "Enable (true) or disable (false) the job"},
				},
				"required": []string{"id"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				id := int64(argInt(args, "id", 0))
				if id <= 0 {
					return "error: a positive id is required", nil
				}
				var req types.UpdateCronJobRequest
				if v, ok := args["name"].(string); ok {
					req.Name = &v
				}
				if v, ok := args["spec"].(string); ok {
					req.Spec = &v
				}
				if v, ok := args["kind"].(string); ok {
					req.Kind = &v
				}
				if v, ok := args["target"].(string); ok {
					req.Target = &v
				}
				if v, ok := args["prompt"].(string); ok {
					req.Prompt = &v
				}
				if v, ok := args["input"].(string); ok {
					req.Input = &v
				}
				if v, ok := args["enabled"].(bool); ok {
					req.Enabled = &v
				}
				job, err := a.Cron.Update(ctx, rc.userDB, id, req)
				if err != nil {
					return "error: " + err.Error(), nil
				}
				state := "enabled"
				if !job.Enabled {
					state = "disabled"
				}
				return fmt.Sprintf("Updated cron job #%d %q (%s, %s, %s).", job.ID, job.Name, job.Kind, job.Spec, state), nil
			},
		}

		reg["cron_run"] = toolEntry{
			def: fnTool("cron_run", "Trigger a cron job to run now (it runs within a minute if enabled). Use to test a job or force an immediate check.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer"},
				},
				"required": []string{"id"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				id := int64(argInt(args, "id", 0))
				if id <= 0 {
					return "error: a positive id is required", nil
				}
				// Mark it due now; the scheduler picks it up on the next tick. This
				// keeps the agent decoupled from the scheduler and off the turn's
				// critical path.
				if err := a.Cron.Reschedule(ctx, rc.userDB, id, time.Now()); err != nil {
					return "error: " + err.Error(), nil
				}
				return fmt.Sprintf("Cron job #%d will run within a minute (if enabled); check it with cron_list.", id), nil
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
			rcx := a.jsRunContext(ctx, rc)
			rcx.Globals = map[string]any{"args": args}
			res, err := a.Runner.Run(run, rcx)
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

// conflictLine renders a detected conflict for a memory tool result, surfacing
// the shared slot key when the conflict came from the structured slot path (so
// the model and user can see *why* two memories collided, e.g. a misattributed
// key).
func conflictLine(h memory.ConflictHit) string {
	key := ""
	if h.Key != "" {
		key = " slot_key=" + h.Key
	}
	return fmt.Sprintf("- %s [%s]%s %q (%s)", h.OtherID, h.Relationship, key, strings.TrimSpace(h.OtherContent), h.Reason)
}

// calculatorEntry is the shared calculator tool (offered to the main agent and
// to delegated calls such as WebFetch analysis).
func (a *Agent) calculatorEntry() toolEntry {
	return toolEntry{
		def: fnTool("calculator", `Evaluate a single arithmetic/JavaScript expression and return the result. Examples: "2 + 2 * 10", "(1500 * 1.08).toFixed(2)", "Math.sqrt(144)". ES5 expression syntax; Math is available. On error, returns what went wrong. Do not second-guess this tool's output.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"expression": map[string]any{"type": "string", "description": "The expression to evaluate, e.g. \"3 * (4 + 5)\""},
			},
			"required": []string{"expression"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			expr, _ := args["expression"].(string)
			expr = strings.TrimSpace(expr)
			if expr == "" {
				return "error: expression is required", nil
			}
			// Wrap as a return so the expression's value is captured (the runner
			// wraps code in a function body, where a bare expression yields nothing).
			res, err := a.Runner.Run("return ("+expr+");", jsrun.RunContext{})
			if err != nil {
				return fmt.Sprintf("error evaluating %q: %v", expr, err), nil
			}
			if res.Value == nil {
				if out := strings.TrimSpace(res.Output); out != "" {
					return out, nil
				}
				return fmt.Sprintf("error: %q did not evaluate to a value", expr), nil
			}
			b, err := json.Marshal(res.Value)
			if err != nil {
				return fmt.Sprintf("%v", res.Value), nil
			}
			return string(b), nil
		},
	}
}

func fnTool(name, desc string, params map[string]any) llm.Tool {
	return llm.Tool{
		Type:     "function",
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
