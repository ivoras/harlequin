-- Skills as first-class DB objects (personal/user scope). A user's own skills and
-- their overrides of shared skills live here. Replaces the old skill_overrides
-- layer.

CREATE TABLE IF NOT EXISTS skills (
    name        TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT '',
    updated_by  INTEGER,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS skill_files (
    skill_name TEXT NOT NULL REFERENCES skills(name) ON DELETE CASCADE,
    relpath    TEXT NOT NULL,
    content    BLOB NOT NULL,
    PRIMARY KEY (skill_name, relpath)
);

-- Back-fill from the previous override tables (kept in place, now unused).
INSERT OR IGNORE INTO skills(name, description, updated_at)
    SELECT skill_name, '', updated_at FROM skill_overrides;
INSERT OR IGNORE INTO skill_files(skill_name, relpath, content)
    SELECT o.skill_name, f.relpath, f.content
    FROM skill_override_files f JOIN skill_overrides o ON o.id = f.override_id;
