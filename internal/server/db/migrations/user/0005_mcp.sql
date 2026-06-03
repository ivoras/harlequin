-- Per-user MCP server registrations (same shape as the shared table; header
-- credentials here are the user's own), plus per-user OAuth tokens for BOTH
-- shared- and user-scoped OAuth servers (each user authorizes individually).
CREATE TABLE IF NOT EXISTS mcp_servers (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    url             TEXT NOT NULL,
    transport       TEXT NOT NULL DEFAULT 'http',
    auth_type       TEXT NOT NULL DEFAULT 'none',
    auth_secret_enc BLOB,
    oauth_meta      TEXT,
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_by      INTEGER,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- OAuth tokens, encrypted at rest, keyed by the (scope, server_name) of the
-- registration they authorize. scope distinguishes a shared server's tokens
-- from a user server's with the same name.
CREATE TABLE IF NOT EXISTS mcp_oauth_tokens (
    scope             TEXT NOT NULL,       -- 'shared' | 'user'
    server_name       TEXT NOT NULL,
    access_token_enc  BLOB,
    refresh_token_enc BLOB,
    token_type        TEXT,
    expiry            TIMESTAMP,
    scopes            TEXT,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (scope, server_name)
);
