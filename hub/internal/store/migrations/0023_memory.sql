-- Shared agent memory (package memory): small, verbatim lessons any agent writes and every agent reads.
-- The hub is the write path and the outbox: a row is kept here and forwarded to TaskAI (one wiki page per
-- memory), retried until it lands. Search for the context pack runs on these rows, merged with TaskAI's.
CREATE TABLE IF NOT EXISTS memories (
  id            INTEGER PRIMARY KEY,
  kind          TEXT NOT NULL,                -- fact | decision | gotcha | runbook | handoff
  title         TEXT NOT NULL,
  body          TEXT NOT NULL,
  repo          TEXT NOT NULL DEFAULT '',     -- owner/name
  issue         INTEGER NOT NULL DEFAULT 0,
  pr            INTEGER NOT NULL DEFAULT 0,
  folder        TEXT NOT NULL DEFAULT '',
  files         TEXT NOT NULL DEFAULT '[]',   -- JSON array of repo-relative paths
  agent         TEXT NOT NULL DEFAULT '',     -- who wrote it: claude-code, codex, fleet-issue-12, a person …
  provider      TEXT NOT NULL DEFAULT '',
  host          TEXT NOT NULL DEFAULT '',
  session       TEXT NOT NULL DEFAULT '',
  confidence    TEXT NOT NULL DEFAULT 'medium',
  supersedes    INTEGER NOT NULL DEFAULT 0,   -- the memory this one replaces
  superseded_by INTEGER NOT NULL DEFAULT 0,
  status        TEXT NOT NULL DEFAULT 'active', -- active | superseded
  expires_at    TEXT NOT NULL DEFAULT '',
  hash          TEXT NOT NULL,                -- of the normalized title + body + scope, for exact duplicates
  source        TEXT NOT NULL DEFAULT 'api',  -- api | import
  source_path   TEXT NOT NULL DEFAULT '',     -- import: the file it came from (with host)
  page_id       INTEGER NOT NULL DEFAULT 0,   -- TaskAI wiki page
  sync_state    TEXT NOT NULL DEFAULT 'pending', -- pending | synced | off
  attempts      INTEGER NOT NULL DEFAULT 0,
  next_try      TEXT NOT NULL DEFAULT '',
  last_error    TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS memories_status ON memories (status, created_at);
CREATE INDEX IF NOT EXISTS memories_sync ON memories (sync_state, next_try);
CREATE INDEX IF NOT EXISTS memories_hash ON memories (hash);
CREATE INDEX IF NOT EXISTS memories_source ON memories (host, source_path);
