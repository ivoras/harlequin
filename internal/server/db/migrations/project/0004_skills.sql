-- Skills as first-class DB objects (project scope). Skills created inside a
-- project live here, visible to all its members and resolved ahead of shared and
-- user skills (project -> shared -> user).

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
