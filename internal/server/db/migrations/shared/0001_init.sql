-- Harlequin shared (org-level) database: shared memories, documents, org skills,
-- org-level MCP servers. Scope is implicit: every row here is org/shared-scoped.

CREATE TABLE IF NOT EXISTS memories (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    content    TEXT NOT NULL,
    metadata   TEXT,
    source     TEXT NOT NULL DEFAULT 'manual',
    pinned     INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Flagged contradictory/duplicate memory pairs. Endpoints are composite ids
-- ("u.<id>" / "s.<id>"); no FK is possible because they may live in different
-- database files.
CREATE TABLE IF NOT EXISTS memory_conflicts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_a      TEXT NOT NULL,
    memory_b      TEXT NOT NULL,
    relationship  TEXT NOT NULL CHECK (relationship IN ('conflicts', 'duplicate')),
    reason        TEXT NOT NULL DEFAULT '',
    confidence    INTEGER NOT NULL,
    detected_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at   TIMESTAMP,
    CHECK (memory_a < memory_b),
    UNIQUE (memory_a, memory_b)
);

-- Structured (key, value) slots extracted from shared memories, used as a
-- precise duplicate/conflict signal. memory_id references a memory in this same
-- file (no FK: memory_slots_vec rows are cleaned up in Go, like FTS/vector).
CREATE TABLE IF NOT EXISTS memory_slots (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_id  INTEGER NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_memory_slots_key ON memory_slots(key);
CREATE INDEX IF NOT EXISTS idx_memory_slots_memory ON memory_slots(memory_id);

-- Org RAG corpus. created_by references a user in the system database (no FK).
CREATE TABLE IF NOT EXISTS documents (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title      TEXT NOT NULL DEFAULT '',
    uri        TEXT NOT NULL DEFAULT '',
    mime       TEXT NOT NULL DEFAULT 'text/plain',
    created_by INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS doc_chunks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    ord         INTEGER NOT NULL DEFAULT 0,
    content     TEXT NOT NULL,
    metadata    TEXT
);
CREATE INDEX IF NOT EXISTS idx_doc_chunks_document ON doc_chunks(document_id);

-- Org-published skill overrides (one per skill name).
CREATE TABLE IF NOT EXISTS skill_overrides (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    skill_name   TEXT NOT NULL UNIQUE,
    published_by INTEGER,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS skill_override_files (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    override_id INTEGER NOT NULL REFERENCES skill_overrides(id) ON DELETE CASCADE,
    relpath     TEXT NOT NULL,
    content     BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_skill_override_files_override ON skill_override_files(override_id);

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
