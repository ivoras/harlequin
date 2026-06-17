-- Harlequin per-project database: one file per project under
-- data/projects/<id>/project.db. Scope is implicit: every row belongs to that
-- project, visible to all its members. Mirrors the user+shared schemas so the
-- existing session/memory/document stores operate on it unchanged.

-- Project memories (composite id "p.<localid>"; the active project supplies the
-- DB, like "u." for a user).
CREATE TABLE IF NOT EXISTS memories (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    content    TEXT NOT NULL,
    metadata   TEXT,
    source     TEXT NOT NULL DEFAULT 'manual',
    pinned     INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

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

CREATE TABLE IF NOT EXISTS memory_slots (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_id  INTEGER NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_memory_slots_key ON memory_slots(key);
CREATE INDEX IF NOT EXISTS idx_memory_slots_memory ON memory_slots(memory_id);

-- Project sessions: a session assigned to the project is moved here from its
-- owner's user.db. owner_user_id records who originally created it.
CREATE TABLE IF NOT EXISTS sessions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_user_id INTEGER NOT NULL DEFAULT 0,
    title         TEXT NOT NULL DEFAULT '',
    hat           TEXT,
    api           TEXT NOT NULL DEFAULT 'REST',
    interface     TEXT NOT NULL DEFAULT 'TUI',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    role            TEXT NOT NULL,
    content         TEXT NOT NULL DEFAULT '',
    tool_calls      TEXT,
    tool_call_id    TEXT,
    name            TEXT,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);

-- Usage accounting for project sessions (acting user recorded in user_id).
CREATE TABLE IF NOT EXISTS usage (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id        INTEGER,
    user_id           INTEGER,
    provider          TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    est_cost_usd      REAL NOT NULL DEFAULT 0,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_usage_created ON usage(created_at);

-- Project RAG corpus.
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

-- Project chatroom: every member can post; messages are kept here. user_id is the
-- author (a user in the system db; no cross-file FK).
CREATE TABLE IF NOT EXISTS chat_messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL,
    content    TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_chat_messages_created ON chat_messages(created_at);
