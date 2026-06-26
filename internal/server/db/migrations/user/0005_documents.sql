-- Personal document corpus: a user's own ingested documents, parallel to the
-- shared (org) and project corpora. Searched alongside shared (and the active
-- project) so a user can keep private documents. The FTS5/vec0 virtual tables
-- are created in Go (createVirtualTables) because they depend on the embedding
-- dimension.

CREATE TABLE IF NOT EXISTS documents (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title      TEXT NOT NULL DEFAULT '',
    uri        TEXT NOT NULL DEFAULT '',
    mime       TEXT NOT NULL DEFAULT 'text/plain',
    created_by INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS doc_chunks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    ord         INTEGER NOT NULL DEFAULT 0,
    content     TEXT NOT NULL,
    metadata    TEXT
);
CREATE INDEX IF NOT EXISTS idx_doc_chunks_document ON doc_chunks(document_id);
