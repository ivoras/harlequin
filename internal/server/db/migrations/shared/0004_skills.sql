-- Skills as first-class DB objects (shared/org scope). Baked-in skills are
-- seeded here from the server binary; org admins can edit or add more. Replaces
-- the old skill_overrides layer and the on-disk skills directory.

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

-- Content hashes of the last-seeded baked files, so re-seeding preserves files a
-- human has edited since (mirrors the old on-disk skills.hashes.json manifest).
CREATE TABLE IF NOT EXISTS skill_seed_hashes (
    relpath TEXT PRIMARY KEY,
    hash    TEXT NOT NULL
);

-- Back-fill from the previous override tables (kept in place, now unused).
INSERT OR IGNORE INTO skills(name, description, updated_by, updated_at)
    SELECT skill_name, '', published_by, updated_at FROM skill_overrides;
INSERT OR IGNORE INTO skill_files(skill_name, relpath, content)
    SELECT o.skill_name, f.relpath, f.content
    FROM skill_override_files f JOIN skill_overrides o ON o.id = f.override_id;
