-- Optional hat a skill-kind cron job's session wears ('' = none). No FK: hats
-- live in the shared database; a run whose hat was deleted fails with an error.
ALTER TABLE cron_jobs ADD COLUMN hat TEXT NOT NULL DEFAULT '';
