-- Hats as first-class DB objects (shared/org scope), mirroring skills. Baked-in
-- hats are seeded here from the server binary; replaces the on-disk
-- <data_dir>/hats/ directory (existing disk hats are imported once at startup).

CREATE TABLE IF NOT EXISTS hats (
    name        TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT '',
    updated_by  INTEGER,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- system_prompt.md is the hat's main file (frontmatter: description + visible
-- skills); per-hat skill overrides live under relpath "skills/<skill>/<file>".
CREATE TABLE IF NOT EXISTS hat_files (
    hat_name TEXT NOT NULL REFERENCES hats(name) ON DELETE CASCADE,
    relpath  TEXT NOT NULL,
    content  BLOB NOT NULL,
    PRIMARY KEY (hat_name, relpath)
);

-- Content hashes of the last-seeded baked files, so re-seeding preserves files a
-- human has edited since (same policy as skill_seed_hashes).
CREATE TABLE IF NOT EXISTS hat_seed_hashes (
    relpath TEXT PRIMARY KEY,
    hash    TEXT NOT NULL
);
