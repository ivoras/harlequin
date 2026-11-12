-- The document a memory was derived from (the document→memory bridge): its
-- local id in this same database, since such a memory lives in its document's
-- scope. NULL for memories written by hand, by a tool or from a chat turn. No
-- FK: a deleted document leaves the reference dangling, shown as such.
ALTER TABLE memories ADD COLUMN source_doc INTEGER;
