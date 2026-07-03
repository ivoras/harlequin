# cmd — binaries & dev harnesses

Two production binaries plus a set of single-file dev/test harnesses. The
harnesses are not shipped; they exist to exercise or inspect one subsystem
without standing up the whole server. Most link SQLite/sqlite-vec, so build them
the same way as the server:

    CGO_ENABLED=1 CGO_CFLAGS="-I$(pwd)/third_party/sqlite/include" go run -tags sqlite_fts5 ./cmd/<name> [args]

## Production binaries
- `harlequin-server` — REST + WebSocket API, LLM access, SQLite, agent loop, skills.
- `harlequin` — Bubble Tea TUI client.

## Web-extraction harnesses (the WebFetchDOM tool + `dom` package)
- `domprobe` — fetches a live URL and prints the `dom.RepeatingGroups` candidate
  lists (what WebFetchDOM surfaces with no grep/selector). Verifies list
  discovery on a real page with no LLM. `go run ./cmd/domprobe "<url>"`.
- `webdomshow` — renders, for a **local HTML file**, the exact result WebFetchDOM
  would return for a given `-grep`/`-selector` (with `-context`), plus a terser
  line-oriented projection, and prints the byte size of each against the tool's
  6000-byte result cap. The fastest way to see what the model would see for a
  page and to compare result encodings. No network, no LLM.
  `go run -tags sqlite_fts5 ./cmd/webdomshow -selector "<css>" <file.html>`
  (flags must precede the file arg).
- `webdomeval` — drives the **configured local LLM** (`/v1/chat/completions` from
  server.yaml's provider host) to check whether a given WebFetchDOM result shape
  lets a small model perform a generic list operation over a page (read / count /
  pick items — deliberately not number- or price-specific). Renders the tool's
  records JSON for the most descriptive list and regression-checks it end to end.
  Requires the local model to be running.
  `go run -tags sqlite_fts5 ./cmd/webdomeval <file.html>`.
- `webextracttest` — exercises the `web-monitor` skill's JS end-to-end against a
  fake page (no network): runs the documented setup, baselines, mutates the page,
  and asserts the repeat check finds the change with no LLM.

## Other subsystem harnesses
- `agentprobe` — drives the agent like a real client (full tool-calling loop).
- `aligntest` — exercises the `docalign` engine (the `align_docs` tool's core)
  against a real sqlite corpus and the configured embeddings provider: ingests
  two revisions of one text plus two different texts on the same subject, runs
  both alignment modes, and asserts the expected pair shapes. No chat LLM.
- `crontest` — exercises the cron Store (CRUD + scheduling math).
- `embedsim` — embeds two strings via the configured embeddings provider and
  prints their cosine similarity (`-url`/`-model` to target an arbitrary endpoint).
- `mcptest` — integration harness for the MCP registry.
- `memfeedback` — summarizes `memory_feedback` events in the session logs.
- `projecttest` — exercises the project registry + per-project storage.
- `slottest` — verifies `memory.WriteSlot` keyed-write idempotency.
