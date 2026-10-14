-- hub store. Turso (Rust SQLite) dialect. Times are fixed-width UTC text (2006-01-02T15:04:05.000Z), so
-- comparing the text compares the time. Every row carries the host it came from.

PRAGMA journal_mode = WAL;

-- Ingestion cursor: how far into each source file the hub has read. The offset moves in the same
-- transaction as the rows it produced, so a restart (or a re-sent chunk) never duplicates anything.
CREATE TABLE IF NOT EXISTS files (
  host       TEXT NOT NULL,
  path       TEXT NOT NULL,
  kind       TEXT NOT NULL,            -- claude | codex | runlog | fleetlog
  off        INTEGER NOT NULL DEFAULT 0,
  size       INTEGER NOT NULL DEFAULT 0,
  mtime      TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (host, path)
);

CREATE TABLE IF NOT EXISTS hosts (
  host      TEXT PRIMARY KEY,
  role      TEXT NOT NULL DEFAULT '',  -- serve | ship
  tz        TEXT NOT NULL DEFAULT '',
  running   TEXT NOT NULL DEFAULT '[]', -- JSON array of fleet worker keys alive at last_seen
  last_seen TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS sessions (
  id            INTEGER PRIMARY KEY,
  host          TEXT NOT NULL,
  path          TEXT NOT NULL,
  provider      TEXT NOT NULL,         -- claude | codex
  session_uuid  TEXT NOT NULL DEFAULT '',
  parent_uuid   TEXT NOT NULL DEFAULT '', -- set for Claude subagent transcripts
  project       TEXT NOT NULL DEFAULT '',
  cwd           TEXT NOT NULL DEFAULT '',
  branch        TEXT NOT NULL DEFAULT '',
  model         TEXT NOT NULL DEFAULT '',
  originator    TEXT NOT NULL DEFAULT '',
  title         TEXT NOT NULL DEFAULT '', -- first person-typed prompt
  auto_title    TEXT NOT NULL DEFAULT '', -- summary / ai-title / custom-title
  agent_name    TEXT NOT NULL DEFAULT '', -- claude --name (fleet workers: fleet-<key>)
  first_at      TEXT NOT NULL DEFAULT '',
  last_at       TEXT NOT NULL DEFAULT '',
  file_mtime    TEXT NOT NULL DEFAULT '',
  message_count INTEGER NOT NULL DEFAULT 0,
  UNIQUE (host, path)
);
CREATE INDEX IF NOT EXISTS sessions_recent ON sessions (file_mtime);
CREATE INDEX IF NOT EXISTS sessions_agent ON sessions (host, agent_name);
CREATE INDEX IF NOT EXISTS sessions_parent ON sessions (parent_uuid);

-- One row per content block. (session_id, line_off, idx) is the line's byte offset in its file plus the
-- block's position in the line: re-reading a line can only hit the same key.
CREATE TABLE IF NOT EXISTS messages (
  id          INTEGER PRIMARY KEY,
  session_id  INTEGER NOT NULL,
  line_off    INTEGER NOT NULL,
  idx         INTEGER NOT NULL,
  role        TEXT NOT NULL,
  kind        TEXT NOT NULL,           -- text | thinking | tool_use | tool_result | image
  meta        INTEGER NOT NULL DEFAULT 0,
  ts          TEXT NOT NULL DEFAULT '',
  text        TEXT NOT NULL DEFAULT '',
  tool_name   TEXT NOT NULL DEFAULT '',
  tool_use_id TEXT NOT NULL DEFAULT '',
  tool_input  TEXT NOT NULL DEFAULT '',
  is_error    INTEGER NOT NULL DEFAULT 0,
  raw         TEXT NOT NULL DEFAULT '', -- the JSON line (first block of a line only), images stripped
  UNIQUE (session_id, line_off, idx)
);

CREATE TABLE IF NOT EXISTS runs (
  id          INTEGER PRIMARY KEY,
  host        TEXT NOT NULL,
  path        TEXT NOT NULL,
  key         TEXT NOT NULL,
  kind        TEXT NOT NULL,           -- issue | pr | review | other
  target      TEXT NOT NULL DEFAULT '',
  provider    TEXT NOT NULL DEFAULT '',
  started_at  TEXT NOT NULL,
  ended_at    TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'running', -- running | succeeded | failed | exited
  summary     TEXT NOT NULL DEFAULT '',
  log         TEXT NOT NULL DEFAULT '',
  log_bytes   INTEGER NOT NULL DEFAULT 0,
  prompt      TEXT NOT NULL DEFAULT '',
  session_id  INTEGER,                 -- the worker's own transcript, when found
  UNIQUE (host, path)
);
CREATE INDEX IF NOT EXISTS runs_started ON runs (started_at);
CREATE INDEX IF NOT EXISTS runs_key ON runs (host, key, started_at);
CREATE INDEX IF NOT EXISTS runs_open ON runs (host) WHERE ended_at = '';

-- Scheduler log (logs/fleet.log), one row per line.
CREATE TABLE IF NOT EXISTS events (
  id       INTEGER PRIMARY KEY,
  host     TEXT NOT NULL,
  path     TEXT NOT NULL,
  line_off INTEGER NOT NULL,
  ts       TEXT NOT NULL DEFAULT '',
  level    TEXT NOT NULL DEFAULT 'info',
  text     TEXT NOT NULL,
  UNIQUE (host, path, line_off)
);
CREATE INDEX IF NOT EXISTS events_ts ON events (ts);
