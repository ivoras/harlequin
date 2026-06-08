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
- `internal/server/{config,db,auth,api,llm,embed,jsrun,agent,memory,documents,audit,sessionlog,usage,conversation,skills,userconfig,cron}`
- `internal/server/skills/jstmpl` — PHP-style `<?js ?>` templating (on `jsrun`).
- `internal/client/{config,apiclient,tui,skills}`
- `internal/shared/types` — REST DTOs. `migrations/{system,shared,user}/*.sql` and `skills/` embedded via `embed.FS`.

## Storage (three-tier SQLite, all WAL)
- `internal/server/storage.Manager` owns the handles. `data/harlequin.db` (system: users, api_tokens) and `data/shared.db` (org: shared memories, documents, org skill overrides) are **kept open**. Each user has `data/users/<id>/user.db` (their memories, conversations, messages, usage, audit, user skill overrides, cron jobs, and a generic `config` key/value table), **opened and closed per request** via `WithUser`; `EachUser` fans out for admin aggregation and maintenance sweeps. Uploaded files go to `data/shared_files/` and `data/users/<id>/files/`.
- No cross-file foreign keys: memory ids are composite strings `u.<localid>`/`s.<localid>`, and `memory_conflicts` endpoints/ids are composite (conflicts involving a user memory live in that user's db; shared–shared in `shared.db`).

## Core flows
- **Auth**: email/password login -> issued API bearer token (SHA-256 in DB); middleware injects user/role. Email is the identity (`users.email`). Self-registration (`/auth/register` -> emailed 6-digit magic code -> `/auth/verify`) holds signups in `pending_registrations` until verified; gated by `auth.allow_registration` (default true). Codes go through `internal/server/email` (SMTP, or logged to console when `smtp_host` is empty).
- **Roles** (`types.IsOwner`/`types.IsElevated`): `owner` > `admin` > `user`. Owner is the only role that can create/edit users (`requireOwner`). Owner+admin ("elevated", `requireElevated`) may create/delete shared memories, delete documents, read the audit log, publish skills, and view other users' usage. Ordinary users get their own conversations and user-scoped memories only — the `memory_write` tool refuses `shared` scope for them.
- **Chat**: client POSTs a message -> agent loop composes prompt with resolved skills, calls the LLM provider (routing + fallback), dispatches tool calls (`memory_search`, `memory_write`, `memory_change`, `memory_delete`, `ask_user`, `list_skills`, `load_skill`, `run_js`, `search_docs`, `WebFetch`, skill-defined tools), loops to `max_steps`, streams SSE deltas. `memory_write`/`memory_change` surface detected conflicts in their result so the model can warn the user; `ask_user` emits an `ask_user` SSE event and ends the turn so the user can reply.
- **Memory**: per-user (`user.db`) + shared (`shared.db`), fused into one view via composite ids; hybrid FTS5 + vector search fused with RRF; provenance/TTL/pinning; auto-extraction; conflict detection on write (FTS/vector candidates across both files + LLM judge, confidence ≥ 7) recorded in `memory_conflicts`.
- **Skills**: baked into the binary, deployed to `<data_dir>/skills/` with a hash manifest (unchanged files replaced on update). Resolution precedence: per-user override -> org-published -> deployed. Server is the single source of truth; clients pull/push via `/skill` slash-commands. Skills support inline `<?js ?>` and frontmatter-declared tools.
- **WebFetch** (`internal/server/webfetch`, on by default, `agent.web_fetch.enabled: false` to disable): fetches a URL with browser-like anti-bot measures — realistic Chrome headers, a uTLS Chrome JA3/TLS fingerprint, HTTP/2, a cookie jar, redirect following, request jitter (no headless browser, no CAPTCHA solving) — converts HTML to Markdown (`JohannesKaufmann/html-to-markdown`), then analyses it with a small model (`agent.web_fetch.model`) given only the WebFetch tool. http→https upgrade; 15-minute per-URL cache; SSRF guard blocks private/loopback targets unless `allow_private`. The trajectory logs each fetch (`web_fetch`: url, final_url, cached, fetch_ms, bytes) and the delegated inner LLM call (`delegated_llm_request`/`delegated_llm_response`, labeled `delegate: web_fetch`, with `duration_ms`).
- **Session logging**: full chat trajectory written as JSONL at `<data_dir>/sessions/<user_id>.<conv_id>.jsonl` (ids zero-padded to ≥5 digits). Optional via `sessions.enabled` (default true).
- **Interfaces**: each session is tied to one *interface* (the medium a user talks through) plus its *API* (transport), stored on the conversation (`api`, `interface` columns) and logged in `session_start`. REST clients announce the interface via the `X-Harlequin-Interface` header (TUI → API `REST` / interface `TUI`); cron-started sessions use `Cron`/`Cron`; a planned Telegram bridge sets `Telegram`/`Telegram`. Add an interface by announcing a new header value, or set api/interface directly in a server-side bridge. Constants in `internal/shared/types` (`InterfaceTUI`, `APIREST`, …).
- **Per-user config**: generic key/value `config` table in `user.db`, via `internal/server/userconfig.Store` (Get/Set/Delete/All) and REST `/config`, `PUT|DELETE /config/{key}` (TUI `/config`). Holds small settings that don't warrant their own table — e.g. registering a Telegram connection (`telegram.chat_id`, `telegram.username`).

## Conventions
- Comments should be terse and explain the non-obvious (the *why*, edge cases, gotchas); don't restate what the code plainly says.
- Secrets only in `.env`, never in YAML or code.
- All JS (templating, `run_js`, skill tools) runs through the sandboxed `jsrun` runner: no file/network (except allow-listed `fetch`), hard `vm.Interrupt` timeout, output cap.
- **Autonomous LLM judgments**: any server process that is *not* directly started by the user (background workers, post-turn hooks, scheduled jobs, etc.) and asks the LLM to *judge* whether to take an action must:
  1. **Request a confidence score** — instruct the model to attach an integer `confidence` field (1–10) to every judgment, meaning how sure it is the result is correct and safe to act on without user confirmation.
  2. **Gate on the score** — only accept and act on judgments with `confidence >= 7`. Discard or ignore everything below the threshold.
  3. **Prefer structured output** — use JSON (or an equally machine-parseable format), not free-form prose lines.
  4. **Silence on uncertainty** — if nothing meets the bar, return an empty collection or explicit negative; never persist placeholder/meta text (e.g. "no facts found").
  - Shared helpers live in `internal/server/llm/judge` (`MinConfidence`, `Accept`, `Clamp`, `PromptRules`, `ParseJSONObject`). Domain-specific parsers (e.g. `internal/server/agent/memextract`) embed `judge.PromptRules()` and call `judge.Accept` before side effects.
- Plan of record: `.cursor/plans/harlequin_agent_base_*.plan.md`.
