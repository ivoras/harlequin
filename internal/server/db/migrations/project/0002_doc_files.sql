-- Persisted uploaded files: keep the original (UTF-8) filename and the on-disk
-- relative path under the scope's files/ directory (the on-disk name is a 7-bit
-- ASCII transliteration). Added identically to every scope (user/shared/project)
-- so the documents schema stays uniform.
ALTER TABLE documents ADD COLUMN original_name TEXT;
ALTER TABLE documents ADD COLUMN stored_path TEXT;
