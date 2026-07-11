package agent

import (
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

	"github.com/ivoras/harlequin/internal/server/documents"
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

	reg["Grep"] = a.grepEntry()

	if a.WebFetcher != nil {
		reg["WebFetch"] = a.webFetchEntry()
		reg["WebFetchDOM"] = a.webFetchDOMEntry()
		reg["WebFetchGrep"] = a.webFetchGrepEntry()
	}

	if a.Docs != nil {
		reg["align_docs"] = a.alignDocsEntry()
		reg["list_documents"] = toolEntry{
			def: fnTool("list_documents", `List every document in the corpus (personal, shared, and — in a project session — project documents): scoped id, title, catalogue description, size and date. Use this to resolve a document the user names loosely ("the new EEA regulation", "last year's contract") to its scoped id (u.N / s.N / p.N) before calling align_docs or search_docs with a docs filter. Prefer matching on the description, not just the title — titles are often raw filenames.`, map[string]any{
				"type": "object", "properties": map[string]any{},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				docs, err := a.Docs.ListScoped(ctx, a.Docs.ScopesFor(rc.userDB, rc.projectDB))
				if err != nil {
					return "", err
				}
				if len(docs) == 0 {
					return "(no documents)", nil
				}
				var sb strings.Builder
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
		reg["check_text"] = toolEntry{
			def: fnTool("check_text", `Authoritatively check whether an exact phrase or keyword appears anywhere in ONE document — a literal, exhaustive scan, unlike search_docs (which ranks by relevance and can miss an exact phrase). Use this BEFORE asserting a section is "new", "removed", or "only in" one document/version: a heading-only sighting in an align_docs summary is not proof of absence elsewhere. Returns every matching chunk with a citation and surrounding snippet, or "not found".`, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"doc":  map[string]any{"type": "string", "description": "Scoped document id to search within, e.g. \"p.6\" (or a specific project's doc, e.g. \"p3.17\")"},
					"text": map[string]any{"type": "string", "description": "Exact phrase or keyword to look for (case-insensitive)"},
				},
				"required": []string{"doc", "text"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				doc, errMsg := a.loadDocRef(ctx, rc, argString(args, "doc"))
				if errMsg != "" {
					return errMsg, nil
				}
				text := strings.TrimSpace(argString(args, "text"))
				if text == "" {
					return "error: text is required", nil
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
			def: fnTool("save_doc", `Save a text you produced (a report, analysis, or summary worth keeping) as a document in the corpus, so it can be found later with search_docs. Keep any [d.x.N] citations in the content — they remain linked to their source documents. Omit scope unless the user asks: the default is right (the project corpus in a project session, else the user's personal documents). Scope "shared" (org-wide, visible to all users) is available to owners/admins only.`, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":   map[string]any{"type": "string", "description": "Document title, e.g. \"Comparison: Regulation 2025 vs 2026\""},
					"content": map[string]any{"type": "string", "description": "The full text to save (Markdown)"},
					"scope":   map[string]any{"type": "string", "enum": []string{"personal", "project", "shared"}},
				},
				"required": []string{"title", "content"},
			}),
			handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
				title := strings.TrimSpace(argString(args, "title"))
				content := strings.TrimSpace(argString(args, "content"))
				if title == "" || content == "" {
					return "error: title and content are required", nil
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
				if problems := a.VerifyCitedQuotes(ctx, rc, content); len(problems) > 0 {
					var sb strings.Builder
					sb.WriteString("error: not saved — problem(s) with quoted claims (every quoted excerpt needs a citation, and it must be to the exact chunk it actually came from, not a similar nearby one):\n")
					for _, p := range problems {
						fmt.Fprintf(&sb, "- %s\n", p)
					}
					sb.WriteString("Fix the citation(s) and call save_doc again.")
					return sb.String(), nil
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
				return fmt.Sprintf("Saved as document %s.%d (%d sections); it is now searchable with search_docs.",
					scopeLetter(scope), doc.ID, doc.Chunks), nil
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
					"id":             map[string]any{"type": "integer"},
					"name":           map[string]any{"type": "string"},
					"spec":           map[string]any{"type": "string", "description": "New cron schedule"},
					"kind":           map[string]any{"type": "string", "enum": []string{"js", "skill"}},
					"target":         map[string]any{"type": "string"},
					"prompt":         map[string]any{"type": "string"},
					"input":          map[string]any{"type": "string", "description": "JSON object of inputs"},
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
	if a.Storage == nil {
		return
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
		log.Printf("save_doc: persist file: %v", err)
		return
	}
	name := fmt.Sprintf("%d-%s.md", doc.ID, documents.AsciiName(doc.Title))
	if werr := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); werr != nil {
		log.Printf("save_doc: write file %q: %v", name, werr)
		return
	}
	if serr := a.Docs.SetStoredPath(ctx, db, doc.ID, name); serr != nil {
		log.Printf("save_doc: set stored_path: %v", serr)
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

// calculatorEntry is the shared calculator tool (offered to the main agent and
// to delegated calls such as WebFetch analysis).
func (a *Agent) calculatorEntry() toolEntry {
	return toolEntry{
		def: fnTool("calculator", `Evaluate a single arithmetic/JavaScript expression and return the result. Examples: "2 + 2 * 10", "(1500 * 1.08).toFixed(2)", "Math.sqrt(144)". ES5.1+ expression syntax; Math is available. On error, returns what went wrong. Do not second-guess this tool's output.`, map[string]any{
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
		fmt.Fprintf(&sb, "] %s\n", strings.TrimSpace(r.Content))
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
