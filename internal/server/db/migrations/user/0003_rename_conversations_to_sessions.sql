-- Rename the "conversation" domain entity to "session": a chat is now a session,
-- and (with WebSocket live sessions) is backed by a long-lived server goroutine.
-- RENAME preserves all rows. SQLite auto-rewrites the messages index and foreign
-- key to track the renamed column; we recreate the index under its new name for
-- tidiness.
ALTER TABLE conversations RENAME TO sessions;

ALTER TABLE messages RENAME COLUMN conversation_id TO session_id;
DROP INDEX IF EXISTS idx_messages_conversation;
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);

ALTER TABLE usage RENAME COLUMN conversation_id TO session_id;
