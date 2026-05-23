ALTER TABLE jobs ADD COLUMN result_committed INTEGER NOT NULL DEFAULT 0 CHECK (result_committed IN (0, 1));

UPDATE jobs
SET result_committed = 1
WHERE status IN ('succeeded', 'failed') AND trim(result_json) NOT IN ('', '{}');
