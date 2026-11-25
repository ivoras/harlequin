-- The model's reasoning behind an assistant message, sent back with the
-- history (see llm.Message.ReasoningContent). Not shown to clients.
ALTER TABLE messages ADD COLUMN reasoning TEXT NOT NULL DEFAULT '';
