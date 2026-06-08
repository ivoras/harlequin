-- Notifications can target a specific session (e.g. the auto-titler's
-- "session-title" control notification carries the renamed conversation's id).
ALTER TABLE notifications ADD COLUMN conversation_id INTEGER;
