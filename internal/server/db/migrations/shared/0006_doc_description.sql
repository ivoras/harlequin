-- Short LLM-generated catalogue description of each document, used to resolve
-- paraphrased document references ("the new EEA regulation").
ALTER TABLE documents ADD COLUMN description TEXT NOT NULL DEFAULT '';
