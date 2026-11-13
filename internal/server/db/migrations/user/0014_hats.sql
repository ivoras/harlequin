-- Personal hats: same shape as shared/0005_hats.sql (no seed-hash table, nothing
-- baked here). A shared hat of the same name shadows a personal one.
CREATE TABLE IF NOT EXISTS hats (
    name        TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT '',
    updated_by  INTEGER,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS hat_files (
    hat_name TEXT NOT NULL REFERENCES hats(name) ON DELETE CASCADE,
    relpath  TEXT NOT NULL,
    content  BLOB NOT NULL,
    PRIMARY KEY (hat_name, relpath)
);
