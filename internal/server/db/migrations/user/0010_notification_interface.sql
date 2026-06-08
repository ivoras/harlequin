-- A notification can target a single interface (e.g. the auto-titler only tells
-- the interface that owns the titled session). NULL = broadcast to any interface.
ALTER TABLE notifications ADD COLUMN target_interface TEXT;
