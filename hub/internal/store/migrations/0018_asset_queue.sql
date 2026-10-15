-- Documents sessions wrote that the hub still has to copy (web/files.go harvester). One row per file; a
-- failed copy is retried with backoff, per host, so one offline Mac doesn't hold up the others.
CREATE TABLE IF NOT EXISTS asset_queue (
  host    TEXT NOT NULL,
  path    TEXT NOT NULL,
  tries   INTEGER NOT NULL DEFAULT 0,
  next_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (host, path)
);
