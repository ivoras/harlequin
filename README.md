# Harlequin

![Harlequin in action](docs/watchprice.gif)

This repo is the answer to the questions I had: why all AI agent harnesses seem to be "local"? How would a "proper" client-server agentic system look like, from the aspects of scalability and common operation expectations?

**You probably don't want to use it**, at least not yet. It's a research project in very early development.

A client-server AI agent system written in Go. A REST + WebSocket **server** communicates with LLMs,
stores data in SQLite (FTS5 + vector search), runs an agentic tool-calling loop, and manages
skills. Chat sessions live on the server (a goroutine per active session) and stream over a
WebSocket, so a client can disconnect mid-turn and reconnect later to resume. A beautiful Bubble
Tea **TUI client** talks to it. Multi-user, organisation-aware.

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

On first start the server creates its SQLite databases and deploys the baked-in skills into
`<data_dir>/skills/`. Accounts are identified by **email address**. Create the first
user — make it an **owner**, the highest role:

```sh
./bin/harlequin-server createuser --config server.yaml --owner --password secret owner@example.com
```

#### Roles

There are three roles, highest privilege first:

| Role | Can do |
|------|--------|
| `owner` | Everything; the **only** role that can create/edit users. |
| `admin` | All org-wide actions: create/delete **shared** memories, delete documents, read the audit log, publish skills, view other users' usage. |
| `user` | Ordinary account: their own sessions and personal (user-scoped) memories only. |

Create further users with `createuser` (`--owner`, `--admin`, or neither for a
plain user), or — once a server is running — via the API as an owner. Only owners
and admins may create or delete shared memories; for ordinary users the
`memory_write` tool refuses `shared` scope and stores the fact as a personal
memory instead.

Change a user's password (revokes their existing API tokens):

```sh
./bin/harlequin-server changepassword alice@example.com --config server.yaml --password newsecret
```

#### Self-registration

Users can also create their own (role `user`) accounts from the TUI or web UI.
Registration is a two-step, email-verified flow:

1. **Register** — submit an email + password; the server emails a 6-digit magic
   code (valid 15 minutes) and holds the signup as *pending* until it's verified.
2. **Verify** — submit the code; the account is created and you're logged in.

In the TUI, type `register` at the email prompt. In the web UI, use **Create an
account** on the sign-in screen.

Self-registration is controlled by `auth.allow_registration` (default `true`; set
`false` to require owner-created accounts). Code delivery uses the `email` SMTP
settings — when `smtp_host` is empty the code is written to the **server console
log** instead, so the flow works in development without a mail server:

```yaml
auth:
  allow_registration: true
email:
  smtp_host: smtp.example.com   # empty -> code is logged to the console
  smtp_port: 587                # 465 = implicit TLS; otherwise STARTTLS
  smtp_username: ""
  smtp_password_env: SMTP_PASSWORD  # env var holding the password (set it in .env / the environment)
  from: ""                      # defaults to smtp_username
```

### Running the client

```sh
./bin/harlequin --config ~/.config/harlequin/client.yaml
```

On first run the TUI prompts for the server URL and your credentials, then stores the issued
API token in the client config.

The TUI client uses truecolor RGB when available (`COLORTERM=truecolor` or equivalent);
older terminals get automatic downgrades to 256-color or 16-color via Lip Gloss and Bubble Tea.

## Interfaces

An **interface** is the medium a user talks to the agent through. Each session (a
chat and its logged trajectory) is tied to exactly one interface, recorded
together with its **API** — the transport the request arrived over:

| Interface | API | Notes |
|-----------|-----|-------|
| `TUI` | `REST` | the built-in text user interface (TUI) client |
| `Telegram` | `Telegram` | planned: a Telegram chatbot bridge |
| `Cron` | `Cron` | internal: scheduled jobs that start an agent turn |

Because the REST + WebSocket API is shared by different clients, a REST client **announces
which interface it is** via the `X-Harlequin-Interface` header (the TUI sends
`TUI`).

Per-user settings a new interface needs live in a generic **`config`** table in
each user's database (key/value). For example, a user registers a Telegram
connection by storing `telegram.chat_id` (and optionally `telegram.username`).
Manage it from the TUI:

```
/config                              list your config
/config set telegram.chat_id 12345   register a Telegram chat
/config rm telegram.chat_id          remove it
```

## Web UI

A static, mobile-first browser client lives in [`web/`](web/) (Svelte 5 + Vite). It
uses the same REST + WebSocket API as the TUI and announces itself as the `Web` interface.

Build it, then have the server serve it at `/` (same origin as the API — no CORS):

```sh
make web-build                       # -> web/dist (needs Node 20+)
```

```yaml
# server.yaml
server:
  web:
    dir: "./web/dist"
```

Now `http://<host>:<addr>/` serves the app and `…/api/v1/…` the API. Leave `web.dir`
empty to disable serving.

**Development:** `make web-dev` runs Vite with hot reload; it proxies `/api` to the
server (set `VITE_API_BASE`, default `http://127.0.0.1:8890`).

**Behind nginx (production):** serve the static build and reverse-proxy the API.
The chat stream is a WebSocket (`…/api/v1/sessions/{id}/ws`) sharing the `/api/`
prefix with ordinary REST calls, so the proxy must forward the upgrade headers —
but only for requests that actually carry an `Upgrade` header. The standard idiom
is a `map` (in the `http {}` context) that yields `Connection: upgrade` for
WebSocket requests and `Connection: close` for the plain REST ones; sending a
static `Connection: upgrade` on every `/api/` request is wrong and breaks regular
requests on some setups.

```nginx
# http {} context (alongside other top-level directives, not inside server{}):
# pick the Connection header value based on whether the request is a WebSocket
# upgrade. Referenced as $connection_upgrade below.
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 80;
    server_name harlequin.example.com;

    root /srv/harlequin/web/dist;
    index index.html;

    # SPA: fall back to index.html for client-side routes.
    location / {
        try_files $uri /index.html;
    }

    # API + WebSocket chat stream (same /api/ prefix; the map handles both).
    location /api/ {
        proxy_pass http://127.0.0.1:8890;
        proxy_http_version 1.1;                          # required for WebSockets
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;          # forward the WS upgrade
        proxy_set_header Connection $connection_upgrade; # upgrade only for WS, else close
        proxy_buffering off;            # stream tokens immediately
        proxy_read_timeout 1h;          # long-lived turns / idle sessions (keep the socket open)
        proxy_send_timeout 1h;
    }
}
```

For TLS (`wss://`), terminate HTTPS on the same `server` block (the SPA uses the
page's scheme, so an `https://` page automatically opens `wss://`); `proxy_pass`
still targets the plaintext local server. With nginx serving the static files,
leave the server's `web.dir` empty.

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
  sandboxed [goja](https://github.com/dop251/goja) VM (ES5.1-compatible, with much of ES6): no
  filesystem, no network (except an allow-listed `fetch`), a hard execution timeout, and an
  output-size cap.
- Session logs under `<data_dir>/sessions/` are plaintext and may contain sensitive conversation
  content. A background task deletes files older than `sessions.retention_days` (default **7**;
  set **0** to keep forever) every hour. Configure redaction in the `sessions:` config block.


# Running with llama.cpp local models

These are just example command lines for starting the LLM server and the embeddings server.

```sh
llama-server -m Qwen3.6-35B-A3B-IQ4_XS-3.53bpw.gguf --port 2234 --host 0.0.0.0 --metrics -c 120000 --timeout 3600 -ctk q8_0 -ctv q8_0 --kv-unified --batch-size 4096 -np 2 --presence-penalty 0.5 --repeat-penalty 1.05 --temperature 0.6 --min_p 0.05 --top_p 0.95 --reasoning-budget 3000 --chat-template-kwargs '{"preserve_thinking": true}' --spec-type ngram-mod --spec-ngram-mod-n-match 24 --spec-ngram-mod-n-min 48 --spec-ngram-mod-n-max 64
```

```sh
llama-server -m granite-embedding-311M-multilingual-r2-Q8_0.gguf --embeddings -c 768 --port 2235
```
