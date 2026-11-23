-- Who last changed each skill file, and when. Set only when a file's content
-- actually changes (see writeItem); no history is kept. Existing files inherit
-- their skill's last update, the best information there is.
ALTER TABLE skill_files ADD COLUMN updated_at TIMESTAMP;
ALTER TABLE skill_files ADD COLUMN updated_by INTEGER;
UPDATE skill_files SET
  updated_at = (SELECT updated_at FROM skills WHERE skills.name = skill_files.skill_name),
  updated_by = (SELECT updated_by FROM skills WHERE skills.name = skill_files.skill_name);
