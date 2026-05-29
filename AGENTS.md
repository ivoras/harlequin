# Harlequin — Agent Guide

Client-server AI agent system in Go. A REST/SSE **server** talks to LLMs, stores data in SQLite, runs an agentic tool-calling loop, and manages skills; a Bubble Tea **TUI client** talks to it. Multi-user, per-organisation.

## Binaries
- `cmd/harlequin-server` — REST/SSE API, LLM access, SQLite, agent loop, skills.
- `cmd/harlequin` — TUI client (Claude Code-like; dark-purple/light-green theme).

## Stack
- Go 1.25. Router `go-chi/chi/v5`; streaming over SSE.
- SQLite via `mattn/go-sqlite3` (CGO, `-tags sqlite_fts5`) + `asg017/sqlite-vec` (`vec0`). Build: `CGO_ENABLED=1 go build -tags sqlite_fts5 ./...` (or `make build`). Compile-time headers are vendored in `third_party/sqlite/`; no system `libsqlite3-dev` needed.
- TUI: `charm.land/{bubbletea,bubbles,lipgloss}/v2`, `glamour` + `chroma` for rendering.
- JS engine: `robertkrimen/otto` (ES5, sandboxed) for `<?js ?>` skill templating and the `run_js` tool.
- Config: YAML (structure) + `.env` (secrets); env overrides YAML.

## Layout
- `internal/server/{config,db,auth,api,llm,embed,jsrun,agent,memory,documents,audit,sessionlog,usage,conversation,skills}`
- `internal/server/skills/jstmpl` — PHP-style `<?js ?>` templating (on `jsrun`).
- `internal/client/{config,apiclient,tui,skills}`
- `internal/shared/types` — REST DTOs. `migrations/*.sql` and `skills/` embedded via `embed.FS`.

## Core flows
- **Auth**: username/password login -> issued API bearer token (SHA-256 in DB); middleware injects user/role.
- **Chat**: client POSTs a message -> agent loop composes prompt with resolved skills, calls the LLM provider (routing + fallback), dispatches tool calls (`memory_search`, `memory_write`, `memory_delete`, `ask_user`, `list_skills`, `load_skill`, `run_js`, `search_docs`, skill-defined tools), loops to `max_steps`, streams SSE deltas. `memory_write` surfaces detected conflicts in its result so the model can warn the user; `ask_user` emits an `ask_user` SSE event and ends the turn so the user can reply.
- **Memory**: per-user + shared; hybrid FTS5 + vector search fused with RRF; provenance/TTL/pinning; auto-extraction; post-write conflict detection (FTS/vector candidates + LLM judge, confidence ≥ 7) stored in `memory_conflicts`.
- **Skills**: baked into the binary, deployed to `<data_dir>/skills/` with a hash manifest (unchanged files replaced on update). Resolution precedence: per-user override -> org-published -> deployed. Server is the single source of truth; clients pull/push via `/skill` slash-commands. Skills support inline `<?js ?>` and frontmatter-declared tools.
- **Session logging**: full chat trajectory written as JSONL under `<data_dir>/sessions/<user>/<conv>.jsonl`.

## Conventions
- Secrets only in `.env`, never in YAML or code.
- All JS (templating, `run_js`, skill tools) runs through the sandboxed `jsrun` runner: no file/network (except allow-listed `fetch`), hard `vm.Interrupt` timeout, output cap.
- **Autonomous LLM judgments**: any server process that is *not* directly started by the user (background workers, post-turn hooks, scheduled jobs, etc.) and asks the LLM to *judge* whether to take an action must:
  1. **Request a confidence score** — instruct the model to attach an integer `confidence` field (1–10) to every judgment, meaning how sure it is the result is correct and safe to act on without user confirmation.
  2. **Gate on the score** — only accept and act on judgments with `confidence >= 7`. Discard or ignore everything below the threshold.
  3. **Prefer structured output** — use JSON (or an equally machine-parseable format), not free-form prose lines.
  4. **Silence on uncertainty** — if nothing meets the bar, return an empty collection or explicit negative; never persist placeholder/meta text (e.g. "no facts found").
  - Shared helpers live in `internal/server/llm/judge` (`MinConfidence`, `Accept`, `Clamp`, `PromptRules`, `ParseJSONObject`). Domain-specific parsers (e.g. `internal/server/agent/memextract`) embed `judge.PromptRules()` and call `judge.Accept` before side effects.
- Plan of record: `.cursor/plans/harlequin_agent_base_*.plan.md`.
