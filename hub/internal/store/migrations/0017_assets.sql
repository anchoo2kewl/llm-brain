-- Assets: the hub's own copy of every file fetched from or dropped onto a host, so it stays downloadable
-- after the host's copy is cleaned up. The bytes live in <state>/assets/<sha256>; rows point at them.
CREATE TABLE IF NOT EXISTS assets (
  id         INTEGER PRIMARY KEY,
  host       TEXT NOT NULL,
  path       TEXT NOT NULL,          -- where it was on the host
  name       TEXT NOT NULL,
  size       INTEGER NOT NULL DEFAULT 0,
  sha256     TEXT NOT NULL,
  source     TEXT NOT NULL DEFAULT 'download', -- download | upload | keep
  created_at TEXT NOT NULL,
  UNIQUE (host, path, sha256)
);
CREATE INDEX IF NOT EXISTS assets_created ON assets (created_at);
