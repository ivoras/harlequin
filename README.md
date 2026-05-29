# Harlequin

This repo is the answer to the questions I had: why all AI agent harnesses seem to be "local"? How would a "proper" client-server agentic system look like, from the aspects of scalability and common operation expectations?

**You probably don't want to use it**, at least not yet. It's in very early development. 

A client-server AI agent system written in Go. A REST/SSE **server** communicates with LLMs,
stores data in SQLite (FTS5 + vector search), runs an agentic tool-calling loop, and manages
skills. A beautiful Bubble Tea **TUI client** talks to it. Multi-user, organisation-aware.

See [AGENTS.md](AGENTS.md) for a thousand-mile architecture overview.

## Build prerequisites

Harlequin uses CGO (for `mattn/go-sqlite3` + `sqlite-vec`) and the SQLite FTS5 build tag.
You need:

- Go 1.25+
- A C toolchain (`gcc`/`clang`) (e.g. `apt install build-essential`)

**No system `libsqlite3-dev` is required.** Compile-time SQLite headers are vendored
under `third_party/sqlite/` (aligned with the `go-sqlite3` embedded engine). The
Makefile sets `CGO_CFLAGS=-Ithird_party/sqlite/include` automatically.

```sh
make build          # builds both binaries into ./bin (CGO_ENABLED=1 -tags sqlite_fts5)
make build-server
make build-client
```

To refresh vendored headers after upgrading `github.com/mattn/go-sqlite3`:

```sh
make vendor-sqlite
```

If you invoke `go build` directly (without `make`), set the include path:

```sh
CGO_ENABLED=1 CGO_CFLAGS="-I$(pwd)/third_party/sqlite/include" go build -tags sqlite_fts5 ./...
```

The first build is slow because it compiles the bundled `sqlite-vec` C extension; later
builds are cached.

## Configuration

Both binaries read a YAML config file plus a `.env` file. YAML holds non-secret structure;
`.env` holds secrets, and environment variables override YAML.

- Server: copy `configs/server.example.yaml` and `.env.example`.
- Client: copy `configs/client.example.yaml`. Client config lives at
  `~/.config/harlequin/client.yaml` by default.

#### Server `.env` secrets

The server loads `.env` from the working directory on startup (via `godotenv`). At minimum
you must set:

| Variable | Required | Purpose |
|----------|----------|---------|
| `JWT_SECRET` | **yes** | Signing key for issued API tokens; use a long random string |

Depending on your LLM/embeddings setup, you may also need:

| Variable | When needed |
|----------|-------------|
| `LLM_API_KEY` | Remote chat providers (e.g. OpenRouter), unless the provider's `api_key_env` in YAML points elsewhere |
| `OPENROUTER_API_KEY` | When using OpenRouter and `api_key_env: OPENROUTER_API_KEY` in `server.yaml` |
| `EMBED_API_KEY` | Remote embeddings endpoint, if it requires authentication |

Optional:

| Variable | Purpose |
|----------|---------|
| `HARLEQUIN_DB_PATH` | Override SQLite database path (default: `<data_dir>/harlequin.db`) |

Local llama.cpp / embedding servers usually need no API keys — leave those variables empty.
Never commit `.env`; only `.env.example` belongs in git.

### Running the server

```sh
cp .env.example .env                          # fill in secrets
cp configs/server.example.yaml server.yaml    # adjust LLM/embeddings endpoints
./bin/harlequin-server --config server.yaml
```

On first start the server creates its SQLite database and deploys the baked-in skills into
`<data_dir>/skills/`. Create the first admin user:

```sh
./bin/harlequin-server createuser --config server.yaml --admin --password secret admin
```

Change a user's password (revokes their existing API tokens):

```sh
./bin/harlequin-server changepassword alice --config server.yaml --password newsecret
```

### Running the client

```sh
./bin/harlequin --config ~/.config/harlequin/client.yaml
```

On first run the TUI prompts for the server URL and your credentials, then stores the issued
API token in the client config.

## Layout

```
cmd/harlequin-server   server binary
cmd/harlequin          TUI client binary
internal/server/...    server packages
internal/client/...    client packages
internal/shared/types  REST DTOs shared by client and server
migrations             embedded SQL migrations
skills                 baked-in skills (embedded into the server binary)
third_party/sqlite     vendored sqlite3.h (compile-time; see third_party/sqlite/README.md)
```

## Security notes

- All JavaScript (skill `<?js ?>` templating, the `run_js` tool, skill-defined tools) runs in a
  sandboxed [otto](https://github.com/robertkrimen/otto) VM: no filesystem, no network (except an
  allow-listed `fetch`), a hard execution timeout, and an output-size cap.
- Session logs under `<data_dir>/sessions/` are plaintext and may contain sensitive conversation
  content. Configure retention/redaction in the `sessions:` config block.
