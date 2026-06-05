-- Server→user notifications, stored in the owning user's database. A
-- notification has a title/description, an optional prompt the client can run,
-- and an auto_run flag for whether that prompt should run automatically. kind is
-- an optional dedupe/category key (e.g. 'harlequin-onboarding').
CREATE TABLE IF NOT EXISTS notifications (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT,
    title        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    prompt       TEXT,
    auto_run     INTEGER NOT NULL DEFAULT 0,
    status       TEXT NOT NULL DEFAULT 'pending', -- pending | delivered | dismissed
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivered_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_notifications_status ON notifications(status);
