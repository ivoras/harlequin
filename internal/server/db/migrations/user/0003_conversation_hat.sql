-- The hat a conversation is "worn" with (one at a time), stored by name (the
-- stable cross-database reference to a hat defined in the shared database).
-- NULL means no hat: use the default system prompt and the normal skill set.
ALTER TABLE conversations ADD COLUMN hat TEXT;
