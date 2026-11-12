-- Per-project read marker for the team chat: the highest chat message id this
-- user has seen. No FK (the project lives in another database); rows of
-- projects the user has left are ignored.
CREATE TABLE IF NOT EXISTS project_chat_reads (
    project_id   INTEGER PRIMARY KEY,
    last_read_id INTEGER NOT NULL DEFAULT 0,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
