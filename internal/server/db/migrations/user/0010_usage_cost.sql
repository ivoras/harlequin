-- Per-call cost attribution: project sessions' ids live in the project db, so
-- tag them; cost_reported = est_cost_usd came from the provider (not the
-- configured price table); cached prompt tokens for cache accounting.
ALTER TABLE usage ADD COLUMN project_id INTEGER;
ALTER TABLE usage ADD COLUMN cached_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage ADD COLUMN cost_reported INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_usage_session ON usage(session_id);
