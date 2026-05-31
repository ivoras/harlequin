-- Structured (key, value) slots extracted from memories, used as a precise
-- duplicate/conflict signal. memory_id references a memory in this same file
-- (no FK: slot index rows in memory_slots_vec are cleaned up in Go, mirroring
-- the FTS/vector handling).
CREATE TABLE IF NOT EXISTS memory_slots (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_id  INTEGER NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_memory_slots_key ON memory_slots(key);
CREATE INDEX IF NOT EXISTS idx_memory_slots_memory ON memory_slots(memory_id);
