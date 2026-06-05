# MCP client

Harlequin can use external **MCP (Model Context Protocol)** servers as tool
sources. The tools an MCP server advertises become callable by the model during a
chat turn, namespaced as `mcp__<server>__<tool>`.

Only **remote Streamable-HTTP** MCP servers are supported (no local stdio
subprocesses). Connections are made by the Harlequin **server**, so all
credentials live server-side.

## Enabling

In `server.yaml`:

```yaml
mcp:
  enabled: true
  allow_user_servers: true
  oauth_callback_base_url: "https://harlequin.example.com"   # required for OAuth servers
```

Credentials (static header values and OAuth tokens) are **encrypted at rest** with
a master key supplied via the environment:

```sh
export HARLEQUIN_SECRET_KEY="$(openssl rand -hex 32)"   # 32 bytes, hex or base64
```

Keep this key stable and backed up: rotating it makes existing stored credentials
undecryptable (re-register affected servers). Without a key, only auth-less
(`none`) servers work; registering a `header`/`oauth` server is rejected.

## Scopes

- **shared** — org-wide, admin-only. Visible to every user. A `header` credential
  is an org-wide service credential. Lives in `shared.db`.
- **user** — per-user. Only the owning user sees it. Lives in that user's `user.db`.

OAuth tokens are **always per-user**, even for a shared server: the admin
registers the server once, then each user authorizes individually.

## Authentication

| `auth_type` | How it connects                                              |
|-------------|--------------------------------------------------------------|
| `none`      | No credentials.                                              |
| `header`    | One or more static request headers, e.g. `Authorization: Bearer <tok>`.|
| `oauth`     | OAuth 2.1 (discovery + PKCE + refresh); per-user tokens.     |

## Commands (in the TUI)

```
/mcp                                   list servers (shared + your own) with status
/mcp show <scope/name>                 show one server
/mcp add <scope/name> <url>            register an auth-less server
/mcp add <scope/name> <url> header Authorization:"Bearer sk-..." [X-Api-Key:"..."]   static header(s)
/mcp add <scope/name> <url> oauth      OAuth server (then authorize)
/mcp test <scope/name>                 connect and list the server's tools
/mcp auth <scope/name>                 start OAuth; prints a URL to open in a browser
/mcp rm <scope/name>                   remove a server
```

`<scope/name>` is e.g. `user/github` or `shared/wiki`; a bare `name` defaults to
`user` scope. Registering or removing `shared` servers requires owner/admin.

### OAuth flow

1. `/mcp add shared/example https://example.com/mcp oauth`
2. `/mcp auth shared/example` — open the printed URL in a browser and approve.
   Harlequin discovers the provider's OAuth endpoints, registers a client
   (dynamic client registration when supported), and completes the exchange at
   `…/api/v1/mcp/oauth/callback`. Tokens are stored encrypted in your `user.db`
   and refreshed automatically.
3. `/mcp test shared/example` — confirm the tools are listed; they're now usable
   in chat.

## Notes

- Sessions are pooled and each server's tool list is cached (`tools_cache_ttl`,
  default 5m), so turns don't re-dial every time.
- A server that needs OAuth you haven't authorized is simply skipped (its tools
  aren't offered to the model); `/mcp` shows it as "needs auth".
- MCP tool calls are logged to the trajectory JSONL (`mcp_call` events) and the
  server console, with scope, server, tool, duration and arguments.
