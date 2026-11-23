-- XML, CSV and TSV uploads were all typed text/plain. They get their own types,
-- so tools can tell an XML e-invoice or a CSV spreadsheet from a note
-- (query_xml, query_sheet, read_doc's large-spreadsheet hint).
UPDATE documents SET mime = 'text/xml'
 WHERE mime = 'text/plain' AND lower(COALESCE(original_name, '')) LIKE '%.xml';
UPDATE documents SET mime = 'text/csv'
 WHERE mime = 'text/plain' AND lower(COALESCE(original_name, '')) LIKE '%.csv';
UPDATE documents SET mime = 'text/tab-separated-values'
 WHERE mime = 'text/plain' AND (lower(COALESCE(original_name, '')) LIKE '%.tsv' OR lower(COALESCE(original_name, '')) LIKE '%.tab');
