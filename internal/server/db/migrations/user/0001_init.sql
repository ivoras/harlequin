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

-- Structured (key, value) slots extracted from memories, used as a precise
-- duplicate/conflict signal. memory_id references a memory in this same file
-- (no FK: slot index rows in memory_slots_vec are cleaned up in Go, mirroring
-- the FTS/vector handling).
CREATE TABLE IF NOT EXISTS memory_slots (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_id  INTEGER NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_memory_slots_key ON memory_slots(key);
CREATE INDEX IF NOT EXISTS idx_memory_slots_memory ON memory_slots(memory_id);

-- A conversation is tied to one interface (the medium a user talks to the agent
-- through) and the API (transport) it arrived over, and optionally a "hat"
-- (worn one at a time, by name — the stable cross-database reference to a hat
-- defined in the shared database; NULL = default prompt + normal skill set).
CREATE TABLE IF NOT EXISTS conversations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title      TEXT NOT NULL DEFAULT '',
    hat        TEXT,
    api        TEXT NOT NULL DEFAULT 'REST',
    interface  TEXT NOT NULL DEFAULT 'TUI',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Tool-result messages carry the tool_call_id (and optionally name) of the
-- assistant tool call they answer, so the conversation replays as a valid
-- OpenAI-compatible message sequence on later turns.
CREATE TABLE IF NOT EXISTS messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role            TEXT NOT NULL,
    content         TEXT NOT NULL DEFAULT '',
    tool_calls      TEXT,
    tool_call_id    TEXT,
    name            TEXT,
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

-- Server→user notifications. A notification has a title/description, an optional
-- prompt the client can run (auto_run = run it automatically), an optional kind
-- (dedupe/category key, e.g. 'harlequin-onboarding'), and optional targeting to
-- a specific session (conversation_id) and/or a single interface
-- (target_interface; NULL = broadcast to any interface).
CREATE TABLE IF NOT EXISTS notifications (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    kind             TEXT,
    title            TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    prompt           TEXT,
    auto_run         INTEGER NOT NULL DEFAULT 0,
    status           TEXT NOT NULL DEFAULT 'pending', -- pending | delivered | dismissed
    conversation_id  INTEGER,
    target_interface TEXT,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivered_at     TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_notifications_status ON notifications(status);

-- Per-user scheduled jobs ("cron"). A job runs a JS script (kind='js') or an
-- agent/skill turn (kind='skill') on a cron schedule with user-provided inputs.
-- next_run_at / last_run_at are Unix epoch seconds (nullable) for unambiguous
-- "due" comparisons in the scheduler.
CREATE TABLE IF NOT EXISTS cron_jobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    spec        TEXT NOT NULL,                 -- cron schedule (5-field / @descriptor / @every)
    kind        TEXT NOT NULL,                 -- 'js' | 'skill'
    target      TEXT NOT NULL DEFAULT '',      -- js: script body or skill://|storage://|tmp:// URI; skill: skill name
    prompt      TEXT NOT NULL DEFAULT '',      -- skill: agent message
    input       TEXT NOT NULL DEFAULT '',      -- JSON object of inputs (js: exposed as `args`)
    enabled     INTEGER NOT NULL DEFAULT 1,
    notify      INTEGER NOT NULL DEFAULT 1,    -- notify the user when output changes / on error
    next_run_at INTEGER,                       -- unix seconds
    last_run_at INTEGER,                       -- unix seconds
    last_status TEXT NOT NULL DEFAULT '',      -- 'ok' | 'error'
    last_output TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_due ON cron_jobs(enabled, next_run_at);

-- Generic per-user key/value config for small settings that don't warrant their
-- own table — e.g. registering a Telegram connection (telegram.chat_id,
-- telegram.username, telegram.interface).
CREATE TABLE IF NOT EXISTS config (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
