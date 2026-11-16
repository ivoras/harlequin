-- Which Telegram chat talks as which account. Incoming bot messages are trusted
-- only through this table (the per-user telegram.chat_id config key is
-- user-editable and only addresses outbound notifications).
CREATE TABLE IF NOT EXISTS telegram_links (
    chat_id    INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL UNIQUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
