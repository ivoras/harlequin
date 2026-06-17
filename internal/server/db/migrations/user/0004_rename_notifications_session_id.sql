-- The conversation→session rename (0003) missed the notifications table's
-- conversation_id column, but notify.go now writes session_id. Rename it so
-- inserts (e.g. broadcast alerts, session-title notifications) work.
ALTER TABLE notifications RENAME COLUMN conversation_id TO session_id;
