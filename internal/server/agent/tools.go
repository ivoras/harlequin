package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/decimalcalc"
	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/xlsxedit"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// toolHandler executes a tool call and returns a string result.
type toolHandler func(ctx context.Context, rc *runContext, args map[string]any) (string, error)

// toolEntry pairs a definition with its handler.
type toolEntry struct {
	def     llm.Tool
	handler toolHandler
}

// sortToolDefs orders tool definitions by function name in place. The tools
// array is rendered into the prompt prefix by the chat template, so a stable
// order is required for llama.cpp's prompt-prefix cache to hit across turns
// (the registry is a map, whose iteration order is otherwise random).
func sortToolDefs(defs []llm.Tool) {
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Function.Name < defs[j].Function.Name
	})
}

// buildTools assembles the tool registry for a request: built-ins plus any
// skill-defined tools, resolved for the requesting user.
func (a *Agent) buildTools(ctx context.Context, rc *runContext) map[string]toolEntry {
	reg := map[string]toolEntry{}

	reg["memory_search"] = toolEntry{
		def: fnTool("memory_search", "Search the user's and shared memory and finds remembered facts, preferences, habits and information about the user and their environment. Each hit includes composite id (u.N/s.N) and slot_key when present — use those with memory_change or memory_delete. Pass all_projects: true to also search the memories of every project the user is a member of (hits there are labelled p<project>.N, e.g. p3.5, and are read-only from this session).", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
				"all_projects": map[string]any{"type": "boolean",
					"description": "Also search every project the user is a member of, not only the session's own"},
			},
			"required": []string{"query"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			q, _ := args["query"].(string)
			var legend string
			foreignDBs := map[int64]*sql.DB{}
			if argBool(args, "all_projects", false) {
				foreign, closeAll := a.openForeignProjects(ctx, rc)
				defer closeAll()
				for _, f := range foreign {
					foreignDBs[f.id] = f.db
				}
				legend = projectLegend(foreign)
			}
			// Fuses user + shared (+ the session's project, + any foreign
			// projects) memories; rc.projectDB is nil outside a project session.
			res, err := a.Memory.SearchFusedAcross(ctx, rc.userDB, rc.projectDB, foreignDBs, q, rc.userID, 6)
			if err != nil {
				return "", err
			}
			// Record which memories were surfaced this turn (the candidate set the
			// model may later cite via memory_feedback).
			for _, r := range res {
				rc.memRecalled = appendUnique(rc.memRecalled, r.ID)
			}
			return legend + renderResults(res), nil
		},
	}

	reg["memory_feedback"] = toolEntry{
		def: fnTool("memory_feedback", `Always call this tool after memory_search results are useful for your reply. Call it with the composite memory ids (e.g. u.4, s.7, p.2), then give your answer. Cite only useful memories — not every result, and not facts you judged irrelevant. This tool helps improve memory.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ids":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Composite memory ids that informed the answer, e.g. [\"u.4\",\"s.7\"]"},
				"reason": map[string]any{"type": "string", "description": "Optional: brief note on how they were used."},
			},
			"required": []string{"ids"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			rc.memUsefulCalled = true
			cited := coerceStringSlice(args["ids"])
			accepted := 0
			for _, id := range cited {
				id = strings.TrimSpace(id)
				if id == "" {
					continue
				}
				// Only credit ids that were actually surfaced this turn; ignore
				// hallucinated/stale ones (counted for compliance measurement).
				if containsStr(rc.memRecalled, id) {
					rc.memUseful = appendUnique(rc.memUseful, id)
					accepted++
				} else {
					rc.memUsefulInvalid++
				}
			}
			if accepted == 0 {
				return "Noted (no cited id matched this turn's search results).", nil
			}
			return fmt.Sprintf("Noted %d useful memory(ies).", accepted), nil
		},
	}

	reg["memory_write"] = toolEntry{
		def: fnTool("memory_write", `Store a durable fact in memory.

scope "shared" (org-wide, owner/admin only): organisation identity and org-wide facts (company name, brand, domain, stack, policies, products); plus generic factual statements about the world outside the user's personal concerns (public definitions, standards, geography, science — objective facts not about this individual). Plain "The company name is …" → shared.

scope "user" (default): personal preferences and habits, private or sensitive information, facts about this individual only ("User prefers …", "I like …"). If unsure and you are not owner/admin, use user.

scope "project" (project sessions only; the default there): facts about the project itself — decisions, conventions, deadlines — visible to every member. In a project session, only use "user"/"shared" when the fact is clearly not project-specific.

Only owner/admin may use shared. When you are owner/admin and the user states an org-wide fact, prefer shared over user.

Optionally pass slot_key to file the fact under an exact attribute key (e.g. "user.preferred_currency"); the content is then stored as that slot's value verbatim and no conflict check runs. Omit slot_key for normal free-text facts.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content":  map[string]any{"type": "string"},
				"scope":    map[string]any{"type": "string", "enum": []string{"user", "shared", "project"}},
				"slot_key": map[string]any{"type": "string", "description": "Optional exact slot key, e.g. user.name; content becomes its value."},
			},
			"required": []string{"content"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			content, _ := args["content"].(string)
			scope, _ := args["scope"].(string)
			// In a project session, a fact with no explicit scope goes to the shared
			// project memory (any member may write it). The model can still target
			// "user"/"shared" explicitly.
			if scope == "project" && rc.projectDB == nil {
				return "error: project scope is only available in a project session; use scope \"user\"", nil
			}
			if rc.projectDB != nil && (scope == "" || scope == "project") {
				mem, err := a.Memory.ProjectAdd(ctx, rc.projectDB, content, "tool")
				if err != nil {
					return "", err
				}
				rc.memWritten = append(rc.memWritten, content)
				return fmt.Sprintf("Stored as project memory %s.", mem.ID), nil
			}
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
				// A slot key must not live in both shared and personal memory. If the
				// same attribute already exists in the other scope, refuse (non-admin)
				// or ask the user how to resolve (admin) — never create the duplicate.
				if m, err := a.Memory.CrossScopeSlot(ctx, rc.userDB, scope, key); err == nil && m != nil {
					otherScope := "shared"
					if scope == "shared" {
						otherScope = "personal"
					}
					if !rc.canShareMemory {
						return fmt.Sprintf("error: can't store %q in your personal memory — that attribute already exists in shared memory as %s (%q). A regular user can't duplicate a shared slot; if the shared value is wrong, ask an owner/admin to change it.", key, m.ID, m.Value), nil
					}
					var sb strings.Builder
					fmt.Fprintf(&sb, "Won't store %q yet: the attribute %q already exists in %s memory as %s (%q), and a slot can't be in both shared and personal memory. Use ask_user to ask whether to: (1) cancel storing it in %s memory, or (2) update the existing %s memory %s to %q (call memory_change with id %s).",
						key, m.Key, otherScope, m.ID, m.Value, scope, otherScope, m.ID, content, m.ID)
					if !m.Exact {
						fmt.Fprintf(&sb, " (3) If the user says these are different attributes, store it under a clearly distinct slot_key instead.")
					}
					sb.WriteString(" Do not create the duplicate.")
					return sb.String(), nil
				}
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
		def: fnTool("memory_change", `Replace the content of an existing memory (same composite id; scope unchanged). Identify the memory by id (u.N, s.N or — in a project session — p.N) or slot_key (e.g. organisation.name) from memory_search or /memory — id is preferred if both are known. Use when the user corrects or updates a fact. Prefer this over memory_delete alone when a replacement is known.`, map[string]any{
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
			// Project memories ("p.<n>") live in the project DB.
			if rc.projectDB != nil && strings.HasPrefix(id, "p.") {
				mem, err := a.Memory.ProjectChange(ctx, rc.projectDB, id, content)
				if err != nil {
					return fmt.Sprintf("error: project memory %s not found", id), nil
				}
				rc.memWritten = append(rc.memWritten, content)
				return fmt.Sprintf("Updated memory %s.", mem.ID), nil
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
		def: fnTool("memory_delete", `Delete a memory by id (u.N/s.N, or p.N in a project session) or slot_key from memory_search or /memory. Use when discarding a fact with no replacement, or after memory_change/memory_write stored the replacement. Never delete alone when the user asked to update a value.`, map[string]any{
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
			// Project memories ("p.<n>") live in the project DB.
			if rc.projectDB != nil && strings.HasPrefix(id, "p.") {
				if err := a.Memory.ProjectDelete(ctx, rc.projectDB, id); err != nil {
					return fmt.Sprintf("error: project memory %s not found", id), nil
				}
				return fmt.Sprintf("Deleted memory %s.", id), nil
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

	reg["wear_hat"] = toolEntry{
		def: fnTool("wear_hat", "Put a hat on this session, or take the current one off (empty name). ONLY when the user asks to; never on your own initiative. The hat applies from the user's next message.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "Hat name; empty takes the hat off"},
			},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			name := strings.TrimSpace(argString(args, "name"))
			if err := a.CheckHat(ctx, name); err != nil {
				var names []string
				if hats, lerr := a.Skills.ListHats(ctx); lerr == nil {
					for _, h := range hats {
						names = append(names, h.Name)
					}
				}
				return fmt.Sprintf("error: %v. Available hats: %s", err, strings.Join(names, ", ")), nil
			}
			if err := a.SetSessionHat(ctx, rc.userDB, rc.sessDB, rc.userID, rc.projectID, rc.sessionID, name); err != nil {
				return "error: " + err.Error(), nil
			}
			if name == "" {
				return "Hat taken off. The default prompt and skills apply from the user's next message.", nil
			}
			return fmt.Sprintf("Now wearing the %q hat. Its prompt and skills apply from the user's next message, not this one.", name), nil
		},
	}

	reg["list_skills"] = toolEntry{
		def: fnTool("list_skills", "List available skills with their descriptions.", map[string]any{
			"type": "object", "properties": map[string]any{},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			// Queried live (not rc.skillInfos): skills can change mid-session.
			infos, err := a.Skills.EffectiveSkillInfos(ctx, rc.userDB, rc.projectDB, rc.hat)
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
			sk, err := a.Skills.ResolveEffective(ctx, rc.userDB, rc.projectDB, name, rc.userID, rc.username, rc.hat)
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
		def: fnTool("run_js", `Execute JavaScript in a sandbox and return its output. The engine (goja) is ES5.1-compatible, and also supports much of ES6: let/const, arrow functions, template literals, classes, destructuring, default/rest/spread, for…of, generators, Map/Set, Symbol, Promise, typed arrays, the ** (exponentiation) operator, and BigInt. Emit output with println()/print() or console.log() (console.log/info/warn/error/debug all print a line; objects print as JSON).
BigInt follows the JS spec strictly: raise to a power with the ** operator (e.g. 10n ** 3n) — there is NO BigInt.pow()/.pow() method; and NEVER mix BigInt with Number in one expression (2n + 3, or BigInt added to Math.pow(...), throws "Cannot mix BigInt and other types") — convert explicitly with BigInt(x) / Number(x) first. Also: break/continue only inside a loop or switch, not in a forEach/map callback; toFixed(n) needs 0<=n<=100.
Available helpers: fetch(url) -> {status, body, finalUrl, contentType}; dom.parse(html) -> handle, then dom.query(handle, cssSelector), dom.grep(handle, text), dom.json(handle). dom.query/dom.grep return node objects with fields .tag/.class/.attrs/.text and methods .getAttribute(name) and .textContent (full text); query results are themselves queryable (dom.query(node, sel)). Per-user file stores tmp.* and storage.* (read/write/list/remove/exists); load(uri)/include(uri) for skill://<skill>/<path>, storage://<path>, tmp://<path> scripts.
Your code is the body of a function the runtime already wraps for you: write top-level statements (a bare top-level 'return' is allowed and its value is captured) — do NOT wrap your code in 'function(){...}' (an un-called function runs nothing, and 'function() {' parses as a nameless declaration -> "Unexpected token (").
Pass code inline, OR set script=<uri> to run a saved JavaScript file instead (NOT both). To parse a saved HTML page (e.g. a WebFetchDOM tmp:// handle), that page is DATA, not a script: put JS in 'code' and read it with tmp.read('<handle>'), e.g. code: "var h = dom.parse(tmp.read('page-x.html')); println(dom.query(h, 'div.price').length);". An optional args object is exposed to the script as the global 'args'.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code":   map[string]any{"type": "string", "description": "Inline JS to run (ES5.1+, goja)."},
				"script": map[string]any{"type": "string", "description": "URI of a saved JavaScript file to run instead of code: skill://<skill>/<path>.js, storage://<path>.js, or tmp://<path>.js. NOT an HTML page — read those with tmp.read() inside code."},
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
				// `script` runs a saved JavaScript file. A saved HTML page (e.g. a
				// WebFetchDOM tmp:// handle) is data, not a script — running it as JS
				// fails with "Unexpected token <". Guide the model to the right call.
				if low := strings.ToLower(s); strings.HasSuffix(low, ".html") || strings.HasSuffix(low, ".htm") {
					return fmt.Sprintf("error: %q is an HTML page, not a script. Don't pass it as `script`. To parse it, put JS in `code` and read the file there, e.g. code: \"var h = dom.parse(tmp.read('%s')); println(dom.query(h, '<css>').length);\"", s, strings.TrimPrefix(strings.TrimPrefix(s, "tmp://"), "storage://")), nil
				}
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

	if a.NotifyDispatch != nil {
		reg["notify_channels"] = toolEntry{
			def: fnTool("notify_channels", "List the notification delivery channels currently available to the user: 'inapp' (built-in TUI/web notification, always), 'email' (the account address), and 'telegram' (only if a bot is configured and the user registered a chat id). Use this before asking the user where to be notified.", map[string]any{
				"type": "object", "properties": map[string]any{},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				chans := a.NotifyDispatch.ActiveChannels(ctx, rc.userDB)
				return "available notification channels: " + strings.Join(chans, ", "), nil
			},
		}
	}

	if a.Email.Configured() && a.Docs != nil {
		reg["send_email"] = a.sendEmailEntry()
	}

	reg["Grep"] = a.grepEntry()
	reg["edit_file"] = a.editFileEntry()

	if a.WebSearch.Configured() {
		reg["WebSearch"] = a.webSearchEntry()
	}

	if a.WebFetcher != nil {
		reg["WebFetch"] = a.webFetchEntry()
		reg["WebFetchDOM"] = a.webFetchDOMEntry()
		reg["WebFetchGrep"] = a.webFetchGrepEntry()
		if a.MarkdownConverter != nil {
			reg["MarkdownFetch"] = a.markdownFetchEntry()
		}
	}

	if a.Docs != nil {
		reg["align_docs"] = a.alignDocsEntry()
		reg["list_documents"] = toolEntry{
			def: fnTool("list_documents", `List every document in the corpus (personal, shared, and — in a project session — project documents): scoped id, title, catalogue description, size and date. Use this to resolve a document the user names loosely ("the new EEA regulation", "last year's contract") to its scoped id (u.N / s.N / p.N) before calling align_docs or search_docs with a docs filter. Pass all_projects: true to also list the documents of every project the user is a member of (ids there are p<project>.N, e.g. p3.17 — usable with align_docs, check_text and search_docs from any session). Prefer matching on the description, not just the title — titles are often raw filenames.`, map[string]any{
				"type": "object", "properties": map[string]any{
					"all_projects": map[string]any{"type": "boolean",
						"description": "Also list documents of every project the user is a member of, not only the session's own"},
				},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				scopes := a.Docs.ScopesFor(rc.userDB, rc.projectDB)
				var legend string
				if argBool(args, "all_projects", false) {
					foreign, closeAll := a.openForeignProjects(ctx, rc)
					defer closeAll()
					for _, f := range foreign {
						scopes = append(scopes, documents.ScopeDB{Scope: documents.ProjectScope(f.id), DB: f.db})
					}
					legend = projectLegend(foreign)
				}
				docs, err := a.Docs.ListScoped(ctx, scopes)
				if err != nil {
					return "", err
				}
				if len(docs) == 0 {
					return legend + "(no documents)", nil
				}
				var sb strings.Builder
				sb.WriteString(legend)
				for _, d := range docs {
					fmt.Fprintf(&sb, "- %s.%d %q · %s · %d sections · %s", scopeLetter(d.Scope), d.ID, d.Title, docTypeLabel(d), d.Chunks, d.CreatedAt.Format("2006-01-02"))
					if d.Description != "" {
						fmt.Fprintf(&sb, " — %s", d.Description)
					}
					sb.WriteString("\n")
				}
				return sb.String(), nil
			},
		}
		reg["read_doc"] = toolEntry{
			def: fnTool("read_doc", `Read a document's text (its Markdown rendering) by reference. Use this for documents the user attached to their message — listed at its end as @"u.18/report.pdf" — pass the reference as written (or just the id, u.18). It also reads a text file in your sandbox: pass its path (tmp://combined.md, storage://notes.md; a bare path means tmp://). Returns up to limit characters from offset; when truncated, the result says where to continue. To find something in a long document prefer search_docs with docs=[id] or check_text. For an Excel workbook you want to edit, pass cells true: it shows every sheet as a grid with its name, column letters and row numbers, so you can name the cell to change.`, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"doc":    map[string]any{"type": "string", "description": `Document reference: u.N / s.N / p.N, optionally as written in the prompt, e.g. @"u.18/report.pdf"; or a sandbox file, e.g. tmp://combined.md`},
					"offset": map[string]any{"type": "integer", "description": "Character offset to start from (default 0)"},
					"limit":  map[string]any{"type": "integer", "description": "Maximum characters to return (default 30000)"},
					"cells":  map[string]any{"type": "boolean", "description": "Excel files only: show each sheet as a grid with its name, column letters and row numbers (formulas as text), to find cell addresses for edit_doc"},
				},
				"required": []string{"doc"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				const gridOnlyExcel = "error: the cells grid view exists only for Excel (.xlsx) files; read this document without cells."
				window := func(title, ref, note, text string) string {
					r := []rune(text)
					off := min(max(argInt(args, "offset", 0), 0), len(r))
					lim := argInt(args, "limit", 30000)
					if lim <= 0 {
						lim = 30000
					}
					end := min(off+lim, len(r))
					out := note + fmt.Sprintf("%s (%s) — characters %d–%d of %d:\n\n%s", title, ref, off, end, len(r), string(r[off:end]))
					if end < len(r) {
						out += fmt.Sprintf("\n\n[truncated — call read_doc again with offset=%d to continue]", end)
					}
					return out
				}
				wantGrid := argBool(args, "cells", false)
				if p := strings.TrimSpace(argString(args, "doc")); isSandboxPath(p) {
					if wantGrid {
						return gridOnlyExcel, nil
					}
					text, _, errMsg := a.sourceText(ctx, rc, p)
					if errMsg != "" {
						return errMsg, nil
					}
					return window(filepath.Base(p), "sandbox file "+p, "", text), nil
				}
				doc, errMsg := a.loadDocRef(ctx, rc, argString(args, "doc"))
				if errMsg != "" {
					return errMsg, nil
				}
				db, dbErrMsg := a.docDBForScope(ctx, rc, doc.Scope)
				if dbErrMsg != "" {
					return dbErrMsg, nil
				}
				if wantGrid {
					data, _, mime, ok := a.storedOriginal(ctx, rc, doc)
					if !ok || mime != xlsxMime || !bytes.HasPrefix(data, []byte("PK")) {
						return gridOnlyExcel, nil
					}
					grid, err := xlsxedit.Grid(data)
					if err != nil {
						return "error: " + err.Error(), nil
					}
					return window(doc.Title, docRef(doc), "", grid), nil
				}
				text, err := a.Docs.FullText(ctx, db, doc.ID)
				if err != nil {
					return "", err
				}
				note := ""
				if _, mime, _, _ := a.Docs.StoredFile(ctx, db, doc.ID); strings.HasPrefix(mime, "image/") {
					note = "[This document is an image. "
					if a.Vision {
						note += "It is shown to you inline with the user's message that listed it; the text below is only its filename or OCR text.]\n\n"
					} else {
						note += "The current model cannot view images; the text below is only its filename or OCR text. Tell the user if they need the picture itself understood.]\n\n"
					}
				}
				out := window(doc.Title, docRef(doc), note, text)
				return out, nil
			},
		}
		reg["check_text"] = toolEntry{
			def: fnTool("check_text", `Authoritatively check whether an exact phrase or keyword appears anywhere in ONE document — a literal, exhaustive scan, unlike search_docs (which ranks by relevance and can miss an exact phrase). Use this BEFORE asserting a section is "new", "removed", or "only in" one document/version: a heading-only sighting in an align_docs summary is not proof of absence elsewhere. Returns every matching chunk with a citation and surrounding snippet, or "not found". To look up MANY phrases at once — e.g. a list of names, to learn which pages of a PDF each is on — pass them all as texts in one call instead of one call per phrase: the result is a table of each phrase and its page numbers (exact for PDF documents), ready for pdf_pages. doc may also be a text file in your sandbox (tmp://combined.md; a bare path means tmp://): matches are then reported by line number.`, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"doc":   map[string]any{"type": "string", "description": "Scoped document id to search within, e.g. \"p.6\" (or a specific project's doc, e.g. \"p3.17\"); or a sandbox file, e.g. tmp://combined.md"},
					"text":  map[string]any{"type": "string", "description": "Exact phrase or keyword to look for (case-insensitive)"},
					"texts": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": `Instead of text: many phrases to look up in one call (up to 500), e.g. ["Ana Horvat", "Ivan Kovač"]; returns the pages of each`},
				},
				"required": []string{"doc"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				if texts := argStrings(args, "texts"); len(texts) > 0 {
					if t := strings.TrimSpace(argString(args, "text")); t != "" {
						texts = append([]string{t}, texts...)
					}
					return a.lookupTexts(ctx, rc, strings.TrimSpace(argString(args, "doc")), texts)
				}
				text := strings.TrimSpace(argString(args, "text"))
				if text == "" {
					return "error: text (one phrase) or texts (a list of phrases) is required", nil
				}
				if p := strings.TrimSpace(argString(args, "doc")); isSandboxPath(p) {
					body, _, errMsg := a.sourceText(ctx, rc, p)
					if errMsg != "" {
						return errMsg, nil
					}
					return findInFileText(p, body, text), nil
				}
				doc, errMsg := a.loadDocRef(ctx, rc, argString(args, "doc"))
				if errMsg != "" {
					return errMsg, nil
				}
				db, dbErrMsg := a.docDBForScope(ctx, rc, doc.Scope)
				if dbErrMsg != "" {
					return dbErrMsg, nil
				}
				hits, err := a.Docs.FindText(ctx, db, doc.ID, text, 5)
				if err != nil {
					return "", err
				}
				if len(hits) == 0 {
					return fmt.Sprintf("Not found: %q does not appear anywhere in %q (%s).", text, doc.Title, docRef(doc)), nil
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "Found %d occurrence(s) of %q in %q:\n", len(hits), text, doc.Title)
				for _, h := range hits {
					cite := fmt.Sprintf("d.%s.%d", scopeLetter(doc.Scope), h.ChunkID)
					if h.Page > 0 {
						cite += fmt.Sprintf(" p.%d", h.Page)
					}
					fmt.Fprintf(&sb, "[%s] …%s…\n", cite, h.Snippet)
				}
				return sb.String(), nil
			},
		}
		reg["search_docs"] = toolEntry{
			def: fnTool("search_docs", `Search the organisation document corpus (RAG) — personal, shared, and (in a project session) project documents. Results are ranked chunks labelled with scope and document chunk id (d.u.N / d.s.N / d.p.N). Pass docs (scoped ids from list_documents, e.g. ["p.3","p.4"]) to search only within those documents — do this whenever the user asks about a specific document or compares specific documents. Pass all_projects: true to additionally search every project you are a member of (hits there are labelled d.p<project>.N, e.g. d.p3.17) — use it when the user asks where something is written down and the current scopes come up empty, or explicitly asks across projects. If the first query returns irrelevant or empty results, retry with synonyms and related terms (e.g. HQ → headquarters, seat, Brussels) before concluding the corpus lacks the answer.`, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
					"docs": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
						"description": "Optional scoped document ids to restrict the search to, e.g. [\"p.3\",\"p.4\"]"},
					"all_projects": map[string]any{"type": "boolean",
						"description": "Also search every project the user is a member of, not only the session's own"},
				},
				"required": []string{"query"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				q, _ := args["query"].(string)
				docsByScope, errMsg := parseDocRefs(coerceStringSlice(args["docs"]))
				if errMsg != "" {
					return errMsg, nil
				}
				// Fuse the org corpus with the user's personal docs and, in a
				// project session, the project corpus (rc.projectDB is set only
				// then). Results are scope-labelled.
				scopes := a.Docs.ScopesFor(rc.userDB, rc.projectDB)
				var legend string
				if argBool(args, "all_projects", false) {
					foreign, closeAll := a.openForeignProjects(ctx, rc)
					defer closeAll()
					for _, f := range foreign {
						scopes = append(scopes, documents.ScopeDB{Scope: documents.ProjectScope(f.id), DB: f.db})
					}
					legend = projectLegend(foreign)
				}
				res, err := a.Docs.SearchScopedDocs(ctx, scopes, q, 6, docsByScope)
				if err != nil {
					return "", err
				}
				return legend + renderDocResults(res), nil
			},
		}
	}

	if a.Docs != nil {
		reg["save_doc"] = toolEntry{
			def: fnTool("save_doc", `Save a text you produced (a report, analysis, or summary worth keeping) as a document in the corpus, so it can be found later with search_docs. Pass the text either inline as content, or — for anything long (a combined or converted document, a fetched page) — as source: a file you already built in your sandbox, e.g. "tmp://combined.md" (write it with run_js's tmp.write, or save a fetched page with WebFetchDOM's save_file); the file is read server-side so the text never has to pass through your output. Pass exactly one of content or source. Keep any [d.x.N] citations in the content — they remain linked to their source documents. Omit scope unless the user asks: the default is right (the project corpus in a project session, else the user's personal documents). Scope "shared" (org-wide, visible to all users) is available to owners/admins only. Set is_raw to true only when the content is original writing you authored (e.g. a story, a draft, freeform prose) rather than a report drawn from the corpus — this skips the quoted-citation check, so any quotation marks in the text are treated as ordinary prose, not as excerpts that must cite a source document.`, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":   map[string]any{"type": "string", "description": "Document title, e.g. \"Comparison: Regulation 2025 vs 2026\""},
					"content": map[string]any{"type": "string", "description": "The full text to save (Markdown). Omit when source is given."},
					"source":  map[string]any{"type": "string", "description": "Sandbox file to save instead of inline content, e.g. tmp://combined.md or storage://report.md (UTF-8 text/Markdown). Omit when content is given."},
					"scope":   map[string]any{"type": "string", "enum": []string{"personal", "project", "shared"}},
					"is_raw":  map[string]any{"type": "boolean", "description": "True if content is original writing you authored (not sourced from the corpus); skips the quoted-citation check. Default false."},
				},
				"required": []string{"title"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				title := strings.TrimSpace(argString(args, "title"))
				content := strings.TrimSpace(argString(args, "content"))
				if src := strings.TrimSpace(argString(args, "source")); src != "" {
					if content != "" {
						return "error: pass either content or source, not both", nil
					}
					root, _, rel, err := a.resolveNamespaceRoot(rc, src)
					if err != nil {
						return "error: " + err.Error(), nil
					}
					data, err := root.Read(rel)
					if err != nil {
						return fmt.Sprintf("error: cannot read source %s: %v", src, err), nil
					}
					if !utf8.Valid(data) {
						return fmt.Sprintf("error: source %s is not UTF-8 text; save_doc stores text/Markdown only", src), nil
					}
					content = strings.TrimSpace(string(data))
				}
				content = documents.StripSoftHyphens(content)
				if title == "" || content == "" {
					return "error: title and content (or a non-empty source file) are required", nil
				}
				scope, _ := args["scope"].(string)
				if scope == "" {
					scope = documents.ScopePersonal
					if rc.projectDB != nil {
						scope = documents.ScopeProject
					}
				}
				db := rc.userDB
				switch scope {
				case documents.ScopeProject:
					if rc.projectDB == nil {
						return "error: project scope is only available in a project session; use scope \"personal\"", nil
					}
					db = rc.projectDB
				case documents.ScopeShared:
					if !rc.canShareMemory {
						return "error: only owner or admin users can save shared documents; use scope \"personal\" instead, or ask an owner/admin.", nil
					}
					db = a.Docs.SharedDB()
				}
				if !argBool(args, "is_raw", false) {
					if problems := a.VerifyCitedQuotes(ctx, rc, content); len(problems) > 0 {
						var sb strings.Builder
						sb.WriteString("error: not saved — problem(s) with quoted claims (every quoted excerpt needs a citation, and it must be to the exact chunk it actually came from, not a similar nearby one):\n")
						for _, p := range problems {
							fmt.Fprintf(&sb, "- %s\n", p)
						}
						sb.WriteString("Fix the citation(s) and call save_doc again.")
						return sb.String(), nil
					}
				}
				// Pin project chunk citations to this project (d.p.N → d.p<id>.N):
				// a bare p resolves against the READER's session project, so a
				// report opened from another context would mis-link its sources.
				if rc.projectID != 0 {
					content = pinProjectCitations(content, rc.projectID)
				}
				doc, err := a.Docs.IngestInto(ctx, db, types.CreateDocumentRequest{
					Title: title, URI: "agent://save_doc", Mime: "text/markdown", Content: content,
				}, rc.userID)
				if err != nil {
					return "", err
				}
				// Persist the exact text to disk too, same as PDF/DOCX uploads: view/
				// download then serves the original bytes verbatim via /file, instead of
				// reconstructing it from chunk boundaries (which don't preserve original
				// whitespace and can visibly mangle content, e.g. splitting a citation).
				a.storeSavedDocFile(ctx, db, scope, rc, doc, content)
				a.publishDocChange(docevents.Created, scope, rc, doc.ID)
				return fmt.Sprintf("Saved as document %s.%d (%d sections); it is now searchable with search_docs.",
					scopeLetter(scope), doc.ID, doc.Chunks), nil
			},
		}

		reg["edit_doc"] = a.editDocEntry()
		reg["create_excel"] = a.createExcelEntry()
		reg["create_pdf"] = a.createPDFEntry()
		reg["create_word"] = a.createWordEntry()
		reg["copy_doc"] = a.copyDocEntry()
		reg["pdf_pages"] = a.pdfPagesEntry()
		a.registerProjectTools(reg, rc)

		reg["delete_doc"] = toolEntry{
			def: fnTool("delete_doc", `Permanently delete a document from the corpus, given its scoped id (u.N / s.N / p.N — resolve it with list_documents first). This removes the document and all its chunks; it cannot be undone, and search_docs/align_docs will no longer find it. Only delete when the user has clearly asked to; if you are unsure which document they mean, use list_documents and confirm with ask_user first. Personal (u.N) documents are always yours to delete; a project (p.N) document is deleted from the current project's corpus; a shared (s.N) document may be deleted by owners/admins only.`, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"doc": map[string]any{"type": "string", "description": "Scoped document id to delete, e.g. \"u.12\", \"s.3\" or \"p.7\""},
				},
				"required": []string{"doc"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				doc, errMsg := a.loadDocRef(ctx, rc, argString(args, "doc"))
				if errMsg != "" {
					return errMsg, nil
				}
				// Authorize by scope, mirroring the API's delete handler: shared
				// deletes are owner/admin-only, and another project's corpus
				// (project:<id>) is off-limits — only the session's own project.
				switch doc.Scope {
				case documents.ScopeShared:
					if !rc.canShareMemory {
						return "error: only owner or admin users can delete shared documents; ask an owner/admin.", nil
					}
				case documents.ScopePersonal, documents.ScopeProject:
					// allowed: the user's own corpus, or the session project's
				default:
					return "error: this document belongs to another project's corpus and cannot be deleted from this session.", nil
				}
				db, dbErrMsg := a.docDBForScope(ctx, rc, doc.Scope)
				if dbErrMsg != "" {
					return dbErrMsg, nil
				}
				ref := fmt.Sprintf("%s.%d", scopeLetter(doc.Scope), doc.ID)
				// Unlink the persisted original too, mirroring storeSavedDocFile's
				// write; best-effort, so a missing Storage just skips it.
				var filesDir string
				if a.Storage != nil {
					switch doc.Scope {
					case documents.ScopeShared:
						filesDir, _ = a.Storage.SharedFilesDir()
					case documents.ScopeProject:
						filesDir, _ = a.Storage.ProjectFilesDir(rc.projectID)
					default:
						filesDir, _ = a.Storage.UserFilesDir(rc.userID)
					}
				}
				if err := a.Docs.DeleteFrom(ctx, db, filesDir, doc.ID); err != nil {
					return "", err
				}
				a.publishDocChange(docevents.Deleted, doc.Scope, rc, doc.ID)
				return fmt.Sprintf("Deleted document %s (%q); it is no longer in the corpus.", ref, doc.Title), nil
			},
		}
	}

	if a.Cron != nil {
		reg["cron_create"] = toolEntry{
			def: fnTool("cron_create", `Schedule a recurring job for the user.
kind "js": run a JavaScript script with NO AI each time — best for cheap periodic checks like watching a website for changes. target is a script URI (skill://<skill>/<path>, storage://<path>, tmp://<path>) or inline JS (ES5.1+); input is a JSON object exposed to the script as the global 'args'. Inline JS is the body of a function the runtime wraps for you: write top-level statements (top-level 'return' is allowed) and do NOT wrap it in 'function(){...}' (an un-called function runs nothing, and 'function() {' is a syntax error). Prefer pointing target at a saved script URI over a long inline body.
kind "skill": run an agent turn — target is an optional skill name to use, prompt is the message.
spec is a cron schedule: 5-field "min hour dom mon dow", a @descriptor (@hourly, @daily), or "@every 30m".
Example (watch a saved web-monitor check every 30 min): cron_create(name="fzoeu", spec="@every 30m", kind="js", target="skill://web-monitor/lib/check.js", input="{\"name\":\"fzoeu\"}").`, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":   map[string]any{"type": "string"},
					"spec":   map[string]any{"type": "string", "description": "Cron schedule (5-field, @descriptor, or @every <dur>)"},
					"kind":   map[string]any{"type": "string", "enum": []string{"js", "skill"}},
					"target": map[string]any{"type": "string", "description": "js: script URI or inline code; skill: skill name"},
					"prompt": map[string]any{"type": "string", "description": "skill: message to send to the agent"},
					"input":  map[string]any{"type": "string", "description": "JSON object of inputs (js: exposed as args)"},
					"hat":    map[string]any{"type": "string", "description": "skill jobs: hat the run wears (default: the hat worn in this session, if any; \"\" = none)"},
					"notify_channel": map[string]any{"type": "string", "enum": []string{"inapp", "email", "telegram"},
						"description": "Where to deliver a change notification (default inapp). Check notify_channels and ask the user if unspecified."},
				},
				"required": []string{"name", "spec", "kind", "target"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				req := types.CreateCronJobRequest{
					Name:          argString(args, "name"),
					Spec:          argString(args, "spec"),
					Kind:          argString(args, "kind"),
					Target:        argString(args, "target"),
					Prompt:        argString(args, "prompt"),
					Input:         argString(args, "input"),
					NotifyChannel: argString(args, "notify_channel"),
				}
				if h, ok := args["hat"].(string); ok {
					req.Hat = h
				} else if rc.hat != nil && req.Kind == types.CronKindSkill {
					req.Hat = rc.hat.Name // asked for while wearing this hat: run it the same way
				}
				if err := a.CheckHat(ctx, req.Hat); err != nil {
					return "error: " + err.Error(), nil
				}
				job, err := a.Cron.Create(ctx, rc.userDB, req)
				if err != nil {
					return "error: " + err.Error(), nil
				}
				next := "unscheduled"
				if job.NextRunAt != nil {
					next = job.NextRunAt.Format("2006-01-02 15:04")
				}
				hat := ""
				if job.Hat != "" {
					hat = fmt.Sprintf(" wearing the %q hat", job.Hat)
				}
				return fmt.Sprintf("Created cron job #%d %q (%s, %s)%s; next run %s.", job.ID, job.Name, job.Kind, job.Spec, hat, next), nil
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
					if j.Hat != "" {
						fmt.Fprintf(&sb, " hat=%s", j.Hat)
					}
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
					"id":             map[string]any{"type": "integer"},
					"name":           map[string]any{"type": "string"},
					"spec":           map[string]any{"type": "string", "description": "New cron schedule"},
					"kind":           map[string]any{"type": "string", "enum": []string{"js", "skill"}},
					"target":         map[string]any{"type": "string"},
					"prompt":         map[string]any{"type": "string"},
					"input":          map[string]any{"type": "string", "description": "JSON object of inputs"},
					"hat":            map[string]any{"type": "string", "description": "Hat the run wears (skill jobs only); \"\" removes it"},
					"enabled":        map[string]any{"type": "boolean", "description": "Enable (true) or disable (false) the job"},
					"notify_channel": map[string]any{"type": "string", "enum": []string{"inapp", "email", "telegram"}, "description": "Delivery channel for change notifications"},
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
				if v, ok := args["hat"].(string); ok {
					if err := a.CheckHat(ctx, v); err != nil {
						return "error: " + err.Error(), nil
					}
					req.Hat = &v
				}
				if v, ok := args["enabled"].(bool); ok {
					req.Enabled = &v
				}
				if v, ok := args["notify_channel"].(string); ok {
					req.NotifyChannel = &v
				}
				job, err := a.Cron.Update(ctx, rc.userDB, id, req)
				if err != nil {
					return "error: " + err.Error(), nil
				}
				state := "enabled"
				if !job.Enabled {
					state = "disabled"
				}
				if job.Hat != "" {
					state += ", hat " + job.Hat
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

	// Skill-defined tools, namespaced <skill>.<tool>, for the visible (hat-aware)
	// skills (rc.skillInfos: resolved once per turn, before buildTools).
	for _, info := range rc.skillInfos {
		sk, err := a.Skills.ResolveEffective(ctx, rc.userDB, rc.projectDB, info.Name, rc.userID, rc.username, rc.hat)
		if err != nil {
			continue
		}
		for _, td := range sk.Tools {
			a.registerSkillTool(reg, sk.Name, td)
		}
	}

	// External MCP server tools (shared + user), namespaced mcp__<server>__<tool>.
	if a.MCP != nil {
		a.registerMCPTools(ctx, rc, reg)
	}

	return reg
}

// storeSavedDocFile persists save_doc's exact text to disk under the target
// scope's files/ directory, mirroring how PDF/DOCX uploads are handled
// (handleCreateDocument's ingestAndStore) — so /file can serve the original
// bytes verbatim rather than the caller reconstructing content from chunk
// boundaries, which drop original whitespace and can visibly mangle text
// (e.g. splitting a citation across a paragraph break). Best-effort: a
// failure here still leaves the document searchable via its chunks.
func (a *Agent) storeSavedDocFile(ctx context.Context, db *sql.DB, scope string, rc *runContext, doc *types.Document, content string) {
	a.storeDocFile(ctx, db, scope, rc, doc, []byte(content), ".md")
}

// storeDocFile persists a tool-made document's original bytes (the file the
// user downloads) next to uploads, named <id>-<title><ext>.
func (a *Agent) storeDocFile(ctx context.Context, db *sql.DB, scope string, rc *runContext, doc *types.Document, data []byte, ext string) string {
	if a.Storage == nil {
		return ""
	}
	var dir string
	var err error
	switch scope {
	case documents.ScopeProject:
		dir, err = a.Storage.ProjectFilesDir(rc.projectID)
	case documents.ScopeShared:
		dir, err = a.Storage.SharedFilesDir()
	default:
		dir, err = a.Storage.UserFilesDir(rc.userID)
	}
	if err != nil {
		log.Printf("store doc file: %v", err)
		return ""
	}
	base := documents.AsciiName(doc.Title)
	if strings.HasSuffix(strings.ToLower(base), ext) {
		base = base[:len(base)-len(ext)]
	}
	name := fmt.Sprintf("%d-%s%s", doc.ID, base, ext)
	if werr := os.WriteFile(filepath.Join(dir, name), data, 0o600); werr != nil {
		log.Printf("store doc file %q: %v", name, werr)
		return ""
	}
	if serr := a.Docs.SetStoredPath(ctx, db, doc.ID, name); serr != nil {
		log.Printf("store doc file: set stored_path: %v", serr)
	}
	return name
}

// storeGenerated is storeDocFile for a file generated from source text: it also
// keeps that source beside the file, so edit_doc can regenerate it.
func (a *Agent) storeGenerated(ctx context.Context, db *sql.DB, scope string, rc *runContext, doc *types.Document, data []byte, ext string, src genSource) {
	name := a.storeDocFile(ctx, db, scope, rc, doc, data, ext)
	if name == "" {
		return
	}
	if err := writeSidecar(a.docFilesDir(rc, scope), name, src); err != nil {
		log.Printf("store doc file %q: write source: %v", name, err)
	}
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

// findInFileText is check_text for a sandbox file: every line containing needle
// (case-insensitive), the first 5 shown with their line numbers.
func findInFileText(name, body, needle string) string {
	low := strings.ToLower(needle)
	var sb strings.Builder
	n := 0
	for i, line := range strings.Split(body, "\n") {
		if !strings.Contains(strings.ToLower(line), low) {
			continue
		}
		if n++; n <= 5 {
			fmt.Fprintf(&sb, "[line %d] %s\n", i+1, truncateRunes(strings.TrimSpace(line), 300))
		}
	}
	if n == 0 {
		return fmt.Sprintf("Not found: %q does not appear anywhere in %s.", needle, name)
	}
	head := fmt.Sprintf("Found %q on %d line(s) of %s", needle, n, name)
	if n > 5 {
		head += " (first 5 shown)"
	}
	return head + ":\n" + sb.String()
}

// calculatorEntry is the shared calculator tool (offered to the main agent and
// to delegated calls such as WebFetch analysis).
func (a *Agent) calculatorEntry() toolEntry {
	return toolEntry{
		def: fnTool("calculator", `Evaluate one arithmetic expression in EXACT decimal arithmetic (no floating-point rounding error: 0.1 + 0.2 is 0.3) and return the result. Use it for money, prices, VAT, totals, interest and any figure that must be exact. Supports + - * / % (remainder) ^ (or **) with integer exponents, parentheses, unary minus, decimals and 1e3 notation, and the functions round(x, digits) (halves away from zero; a top-level round(x, 2) prints exactly 2 decimals, e.g. "1620.00"), floor, ceil, trunc, abs, min, max. Examples: "round(1500 * 1.08, 2)", "19.99 * 3", "(1 + 0.05) ^ 3", "1 / 3" (non-terminating results are shown to 20 decimals with the exact fraction). Write numbers plainly, without thousands separators or currency symbols (1234.56, not 1,234.56). Anything else (Math.sqrt, fractional powers, methods like .toFixed) is evaluated as floating-point JavaScript and the result says so. On error, returns what went wrong. Do not second-guess this tool's output.`, map[string]any{
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
			// Exact decimal arithmetic first; only syntax outside its grammar
			// (Math.*, methods, non-integer powers) falls back to JS floats.
			out, derr := decimalcalc.Eval(expr)
			if derr == nil {
				return out, nil
			}
			if !errors.Is(derr, decimalcalc.ErrUnsupported) {
				return fmt.Sprintf("error evaluating %q: %v", expr, derr), nil
			}
			// Wrap as a return so the expression's value is captured (the runner
			// wraps code in a function body, where a bare expression yields nothing).
			// "^" is power in this tool but XOR in JS; keep its meaning.
			res, err := a.Runner.Run("return ("+strings.ReplaceAll(expr, "^", "**")+");", jsrun.RunContext{})
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
			return string(b) + "\n(note: evaluated as floating-point JavaScript, not exact decimal; for money use + - * / ^ and round(x, 2) so it is exact)", nil
		},
	}
}

func fnTool(name, desc string, params map[string]any) llm.Tool {
	return llm.Tool{
		Type:     "function",
		Function: llm.FunctionDefinition{Name: name, Description: desc, Parameters: params},
	}
}

// containsStr reports whether s is in xs.
func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// appendUnique appends s to xs only if not already present.
func appendUnique(xs []string, s string) []string {
	if containsStr(xs, s) {
		return xs
	}
	return append(xs, s)
}

// coerceStringSlice extracts a []string from a JSON-decoded tool argument, which
// may arrive as []any of strings (object args) or a single string.
func coerceStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func renderResults(res []types.SearchResult) string {
	if len(res) == 0 {
		return "(no results)"
	}
	var sb strings.Builder
	sb.WriteString("Ranked memory results (prefer #1 when it directly answers the question; use id or slot_key with memory_change/memory_delete; do not invent facts not listed here):\n")
	for i, r := range res {
		fmt.Fprintf(&sb, "%d. [%s", i+1, r.ID)
		if r.Scope != "" {
			fmt.Fprintf(&sb, " %s", r.Scope)
		}
		if len(r.SlotKeys) > 0 {
			fmt.Fprintf(&sb, " {%s}", strings.Join(r.SlotKeys, ", "))
		}
		fmt.Fprintf(&sb, "] %s", strings.TrimSpace(r.Content))
		if r.Source != "" { // derived from a document: where to read more
			fmt.Fprintf(&sb, " (from %s)", r.Source)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func renderDocResults(res []types.SearchResult) string {
	if len(res) == 0 {
		return "(no document results)"
	}
	var sb strings.Builder
	sb.WriteString("Ranked document results (prefer #1 when it directly answers the question; do not invent facts not listed here).\n")
	sb.WriteString("Cite the chunk id inline immediately after any information you use, e.g. \"…is seated in Brussels [d.u.943]\". The client renders each cited id as a link to the source document and its exact position, so always carry the ids into your answer.\n")
	for i, r := range res {
		fmt.Fprintf(&sb, "%d. [%s", i+1, r.ID)
		if r.Scope != "" {
			fmt.Fprintf(&sb, " %s", r.Scope)
		}
		if r.Source != "" {
			fmt.Fprintf(&sb, " · %s", r.Source)
		}
		if r.DocumentID > 0 {
			// Scoped document id (u.N/s.N/p.N), directly usable with align_docs.
			fmt.Fprintf(&sb, " · doc %s.%d", scopeLetter(r.Scope), r.DocumentID)
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
		ref, err := a.Memory.ResolveRef(ctx, rc.userDB, rc.projectDB, id, rc.userID)
		if err != nil {
			return "", memoryRefError(err, "id", id)
		}
		return ref, ""
	}
	if slotKey != "" {
		ref, err := a.Memory.ResolveRef(ctx, rc.userDB, rc.projectDB, slotKey, rc.userID)
		if err != nil {
			return "", memoryRefError(err, "slot_key", slotKey)
		}
		return ref, ""
	}
	return "", "error: id or slot_key is required (from memory_search or /memory)"
}

// bareProjectCite matches an unqualified project chunk citation (d.p.N).
var bareProjectCite = regexp.MustCompile(`\bd\.p\.(\d+)\b`)

// pinProjectCitations qualifies bare project chunk citations with the project
// they actually reference (d.p.N → d.p<id>.N), so they stay resolvable when
// the document is read outside that project's session. Bare document refs
// (p.N) are left alone — the same pattern is used for page numbers in prose.
func pinProjectCitations(content string, projectID int64) string {
	return bareProjectCite.ReplaceAllString(content, "d.p"+strconv.FormatInt(projectID, 10)+".$1")
}

// foreignProject is another project the user belongs to (beyond the session's
// own), opened read-only for the duration of an all-projects search.
type foreignProject struct {
	id   int64
	name string
	db   *sql.DB
}

// openForeignProjects opens every project the user is a member of except the
// session's own. The returned closer must be called when the search is done
// (project databases are not kept open across requests). Best-effort: a
// project that fails to open is skipped.
func (a *Agent) openForeignProjects(ctx context.Context, rc *runContext) ([]foreignProject, func()) {
	noop := func() {}
	if a.Projects == nil || a.Storage == nil {
		return nil, noop
	}
	projs, err := a.Projects.List(ctx, rc.userID)
	if err != nil {
		return nil, noop
	}
	var out []foreignProject
	for _, p := range projs {
		if p.ID == rc.projectID {
			continue
		}
		pdb, err := a.Storage.OpenProjectReadOnly(ctx, p.ID)
		if err != nil {
			continue
		}
		out = append(out, foreignProject{id: p.ID, name: p.Name, db: pdb})
	}
	return out, func() {
		for _, f := range out {
			_ = f.db.Close()
		}
	}
}

// projectLegend names each qualified project prefix for the model, e.g.
// "Other projects searched: p3 = «Alpha», p5 = «Beta»."
func projectLegend(foreign []foreignProject) string {
	if len(foreign) == 0 {
		return "Other projects searched: none (you are not a member of any other project).\n"
	}
	parts := make([]string, len(foreign))
	for i, f := range foreign {
		parts[i] = fmt.Sprintf("p%d = «%s»", f.id, f.name)
	}
	return "Other projects searched: " + strings.Join(parts, ", ") + ". When citing results from them, name the project in your answer.\n"
}

// docTypeLabel renders a document's format (e.g. "PDF", "DOCX", "TXT") from
// its original filename's extension, falling back to the MIME type for
// documents ingested as raw text (no file).
func docTypeLabel(d types.Document) string {
	if ext := strings.TrimPrefix(filepath.Ext(d.OriginalName), "."); ext != "" {
		return strings.ToUpper(ext)
	}
	switch d.Mime {
	case "text/markdown":
		return "MD"
	case "text/plain", "":
		return "TXT"
	case "application/pdf":
		return "PDF"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return "DOCX"
	}
	if i := strings.LastIndex(d.Mime, "/"); i >= 0 {
		return strings.ToUpper(d.Mime[i+1:])
	}
	return strings.ToUpper(d.Mime)
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

// publishDocChange tells connected clients a document changed (no-op without a bus).
func (a *Agent) publishDocChange(op, scope string, rc *runContext, docID int64) {
	ev := docevents.Event{Op: op, Scope: scope, UserID: rc.userID, DocID: docID}
	if scope == documents.ScopeProject {
		ev.ProjectID = rc.projectID
	}
	a.DocEvents.Publish(ev)
}
