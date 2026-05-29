package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
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
		def: fnTool("memory_search", "Search the user's and shared memory for relevant facts.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []string{"query"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			q, _ := args["query"].(string)
			res, err := a.Memory.Search(ctx, q, rc.userID, "", 6)
			if err != nil {
				return "", err
			}
			return renderResults(res), nil
		},
	}

	reg["memory_write"] = toolEntry{
		def: fnTool("memory_write", "Store a durable fact in memory.", map[string]any{
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
			_, err := a.Memory.Add(ctx, types.CreateMemoryRequest{Scope: scope, Content: content, Source: "tool"}, rc.userID)
			if err != nil {
				return "", err
			}
			return "stored", nil
		},
	}

	reg["list_skills"] = toolEntry{
		def: fnTool("list_skills", "List available skills with their descriptions.", map[string]any{
			"type": "object", "properties": map[string]any{},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			infos, err := a.Skills.List(ctx, rc.userID, rc.username)
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
			sk, err := a.Skills.Resolve(ctx, name, rc.userID, rc.username)
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
		def: fnTool("run_js", "Execute JavaScript (ES5) in a sandbox and return its output. Use print() to emit output.", map[string]any{
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

	// Skill-defined tools, namespaced <skill>.<tool>.
	infos, err := a.Skills.List(ctx, rc.userID, rc.username)
	if err == nil {
		for _, info := range infos {
			sk, err := a.Skills.Resolve(ctx, info.Name, rc.userID, rc.username)
			if err != nil {
				continue
			}
			for _, td := range sk.Tools {
				a.registerSkillTool(reg, sk.Name, td)
			}
		}
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
	sb.WriteString("Ranked memory results (prefer #1 when it directly answers the question; do not invent facts not listed here):\n")
	for i, r := range res {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, strings.TrimSpace(r.Content))
	}
	return sb.String()
}
