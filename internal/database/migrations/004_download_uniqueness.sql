DELETE FROM downloads
WHERE id IN (
  SELECT id
  FROM (
    SELECT id,
      row_number() OVER (
        PARTITION BY source, external_id
        ORDER BY
          CASE status
            WHEN 'succeeded' THEN 0
            WHEN 'running' THEN 1
            WHEN 'queued' THEN 2
            WHEN 'failed' THEN 3
            ELSE 4
          END,
          updated_at DESC,
          id DESC
      ) AS duplicate_rank
    FROM downloads
    WHERE external_id <> ''
  )
  WHERE duplicate_rank > 1
);

CREATE UNIQUE INDEX idx_downloads_source_external_id ON downloads(source, external_id) WHERE external_id <> '';
