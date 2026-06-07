-- Per-user scheduled jobs ("cron"). A job runs a JS script (kind='js') or an
-- agent/skill turn (kind='skill') on a cron schedule with user-provided inputs.
-- next_run_at / last_run_at are Unix epoch seconds (nullable) for unambiguous
-- "due" comparisons in the scheduler.
CREATE TABLE IF NOT EXISTS cron_jobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    spec        TEXT NOT NULL,                 -- cron schedule (5-field / @descriptor / @every)
    kind        TEXT NOT NULL,                 -- 'js' | 'skill'
    target      TEXT NOT NULL DEFAULT '',      -- js: script body or skill://|storage://|tmp:// URI; skill: skill name
    prompt      TEXT NOT NULL DEFAULT '',      -- skill: agent message
    input       TEXT NOT NULL DEFAULT '',      -- JSON object of inputs (js: exposed as `args`)
    enabled     INTEGER NOT NULL DEFAULT 1,
    notify      INTEGER NOT NULL DEFAULT 1,    -- notify the user when output changes / on error
    next_run_at INTEGER,                       -- unix seconds
    last_run_at INTEGER,                       -- unix seconds
    last_status TEXT NOT NULL DEFAULT '',      -- 'ok' | 'error'
    last_output TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_cron_jobs_due ON cron_jobs(enabled, next_run_at);
