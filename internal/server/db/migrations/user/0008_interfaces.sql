-- Each session (conversation) is tied to one interface (the medium a user talks
-- to the agent through) and the API (transport) it arrived over. Defaults cover
-- pre-existing rows and the TUI (REST transport, TUI interface).
ALTER TABLE conversations ADD COLUMN api TEXT NOT NULL DEFAULT 'REST';
ALTER TABLE conversations ADD COLUMN interface TEXT NOT NULL DEFAULT 'TUI';

-- Generic per-user key/value config for small settings that don't warrant their
-- own table — e.g. registering a Telegram connection (telegram.chat_id,
-- telegram.username, telegram.interface).
CREATE TABLE IF NOT EXISTS config (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
