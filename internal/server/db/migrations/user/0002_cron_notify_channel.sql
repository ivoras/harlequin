-- The channel a cron job's change-notification is delivered through:
-- 'inapp' (built-in TUI/web notification, default), 'email', or 'telegram'.
ALTER TABLE cron_jobs ADD COLUMN notify_channel TEXT NOT NULL DEFAULT 'inapp';
