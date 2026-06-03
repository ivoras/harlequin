-- Org-level (shared) MCP server registrations. Tools advertised by these servers
-- become available to every user. Header credentials (auth_type='header') are an
-- org-wide service credential, encrypted at rest. OAuth servers store no token
-- here: each user authorizes individually (tokens live in their user.db).
CREATE TABLE IF NOT EXISTS mcp_servers (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    url             TEXT NOT NULL,
    transport       TEXT NOT NULL DEFAULT 'http',   -- 'http' (Streamable HTTP)
    auth_type       TEXT NOT NULL DEFAULT 'none',    -- 'none' | 'header' | 'oauth'
    auth_secret_enc BLOB,                            -- header auth: encrypted JSON {header,value}
    oauth_meta      TEXT,                            -- oauth: non-secret JSON config
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_by      INTEGER,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
