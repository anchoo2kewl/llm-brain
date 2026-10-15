-- Terminal sessions: an interactive Claude Code / Codex / Gemini CLI (or a plain shell) running in tmux on a
-- host, with no approvals and no sandbox. The hub only starts, attaches to and kills them; the tmux session on
-- that host is the source of truth, and it outlives the hub, the browser and the host agent.
CREATE TABLE IF NOT EXISTS terminals (
  id         INTEGER PRIMARY KEY,
  host       TEXT NOT NULL,
  provider   TEXT NOT NULL,               -- claude | codex | gemini | shell
  cwd        TEXT NOT NULL,               -- as asked; the real path once it starts
  model      TEXT NOT NULL DEFAULT '',
  title      TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'starting', -- starting | live | ended | failed
  error      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  ended_at   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS terminals_updated ON terminals (updated_at);
CREATE INDEX IF NOT EXISTS terminals_host_status ON terminals (host, status);
