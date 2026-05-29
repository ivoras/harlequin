-- Flagged contradictory or duplicate memory pairs (LLM-judged, unresolved until resolved_at set).

CREATE TABLE IF NOT EXISTS memory_conflicts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_a      INTEGER NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    memory_b      INTEGER NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    relationship  TEXT NOT NULL CHECK (relationship IN ('conflicts', 'duplicate')),
    reason        TEXT NOT NULL DEFAULT '',
    confidence    INTEGER NOT NULL,
    detected_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at   TIMESTAMP,
    CHECK (memory_a < memory_b),
    UNIQUE (memory_a, memory_b)
);
CREATE INDEX IF NOT EXISTS idx_memory_conflicts_a ON memory_conflicts(memory_a);
CREATE INDEX IF NOT EXISTS idx_memory_conflicts_b ON memory_conflicts(memory_b);
