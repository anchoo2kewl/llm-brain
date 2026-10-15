-- A name given on the hub. Ingest never touches it, so it wins over the first prompt and the CLI's own
-- titles.
ALTER TABLE sessions ADD COLUMN name TEXT NOT NULL DEFAULT '';
