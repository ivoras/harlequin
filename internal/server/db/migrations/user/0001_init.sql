-- Harlequin per-user database: one file per user under data/users/<id>/user.db.
-- Ownership is implicit: every row here belongs to that user.

CREATE TABLE IF NOT EXISTS memories (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    content    TEXT NOT NULL,
    metadata   TEXT,
    source     TEXT NOT NULL DEFAULT 'manual',
    pinned     INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Flagged contradictory/duplicate memory pairs involving this user's memories.
-- Endpoints are composite ids ("u.<id>" / "s.<id>"); no FK across database files.
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

CREATE TABLE IF NOT EXISTS conversations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role            TEXT NOT NULL,
    content         TEXT NOT NULL DEFAULT '',
    tool_calls      TEXT,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id);

-- Usage accounting. conversation_id references a conversation in this same db.
CREATE TABLE IF NOT EXISTS usage (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id   INTEGER,
    provider          TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    est_cost_usd      REAL NOT NULL DEFAULT 0,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_usage_created ON usage(created_at);

CREATE TABLE IF NOT EXISTS audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    action     TEXT NOT NULL,
    target     TEXT NOT NULL DEFAULT '',
    detail     TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);

-- This user's skill overrides (one per skill name).
CREATE TABLE IF NOT EXISTS skill_overrides (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    skill_name TEXT NOT NULL UNIQUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS skill_override_files (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    override_id INTEGER NOT NULL REFERENCES skill_overrides(id) ON DELETE CASCADE,
    relpath     TEXT NOT NULL,
    content     BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_skill_override_files_override ON skill_override_files(override_id);
