-- Page-level provenance: the (1-based) source page a chunk came from, 0 when the
-- document has no page structure (text files). Added identically to every scope.
ALTER TABLE doc_chunks ADD COLUMN page INTEGER NOT NULL DEFAULT 0;
