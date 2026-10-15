-- Session control: Claude/Codex sessions the hub starts (or continues) on a host, the command queue the host
-- agents pull from, the saved prompt library, and which hosts have a live agent.

-- An agent session: one Claude or Codex conversation the hub drives, a turn at a time. provider_session is
-- the CLI's own session id (claude --resume / codex resume), known after the first turn starts; the ingested
-- transcript is found by it.
CREATE TABLE IF NOT EXISTS agent_sessions (
  id               INTEGER PRIMARY KEY,
  host             TEXT NOT NULL,
  cwd              TEXT NOT NULL,
  provider         TEXT NOT NULL,             -- claude | codex
  model            TEXT NOT NULL DEFAULT '',
  perm             TEXT NOT NULL DEFAULT 'acceptEdits', -- plan | acceptEdits | bypass
  bypass_ok        INTEGER NOT NULL DEFAULT 0, -- the person ticked "allow bypass" for this session
  title            TEXT NOT NULL DEFAULT '',
  provider_session TEXT NOT NULL DEFAULT '',
  status           TEXT NOT NULL DEFAULT 'queued', -- queued | running | idle | failed | stopped
  pid              INTEGER NOT NULL DEFAULT 0,
  review_of        INTEGER NOT NULL DEFAULT 0, -- the agent session this one reviews
  adopted          INTEGER NOT NULL DEFAULT 0, -- started outside the hub, continued from it
  scratch          INTEGER NOT NULL DEFAULT 0, -- a chat in its own scratch folder under the host's state dir
  turns            INTEGER NOT NULL DEFAULT 0,
  current_cmd      INTEGER NOT NULL DEFAULT 0, -- the turn command that may move this session's status
  last_result      TEXT NOT NULL DEFAULT '',  -- the last turn's final answer (a review's findings)
  error            TEXT NOT NULL DEFAULT '',
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_sessions_updated ON agent_sessions (updated_at);
-- One agent session per CLI session: continuing a transcript twice finds the same row.
CREATE UNIQUE INDEX IF NOT EXISTS agent_sessions_provider ON agent_sessions (host, provider, provider_session) WHERE provider_session != '';
CREATE INDEX IF NOT EXISTS agent_sessions_review ON agent_sessions (review_of, id);

-- The command queue. Host agents pull their host's queued commands (POST /api/agent/poll) and report
-- back; the hub's own agent reads the table directly. Rows are kept: this is also the command log.
CREATE TABLE IF NOT EXISTS commands (
  id          INTEGER PRIMARY KEY,
  host        TEXT NOT NULL,
  kind        TEXT NOT NULL,                  -- turn | stop
  agent_id    INTEGER NOT NULL DEFAULT 0,
  payload     TEXT NOT NULL DEFAULT '{}',
  status      TEXT NOT NULL DEFAULT 'queued', -- queued | claimed | running | done | failed | stopped | cancelled
  result      TEXT NOT NULL DEFAULT '',       -- the last report, JSON
  created_at  TEXT NOT NULL,
  claimed_at  TEXT NOT NULL DEFAULT '',
  finished_at TEXT NOT NULL DEFAULT '',
  updated_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS commands_host_status ON commands (host, status);
CREATE INDEX IF NOT EXISTS commands_agent ON commands (agent_id);

-- The prompt library: saved prompts with {{VARS}}.
CREATE TABLE IF NOT EXISTS saved_prompts (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  body       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- Hosts with a session agent: when it last polled, its allowed roots and the git folders under them.
CREATE TABLE IF NOT EXISTS agent_hosts (
  host      TEXT PRIMARY KEY,
  version   TEXT NOT NULL DEFAULT '',
  roots     TEXT NOT NULL DEFAULT '[]',
  folders   TEXT NOT NULL DEFAULT '[]',
  providers TEXT NOT NULL DEFAULT '[]', -- agent CLIs installed there: claude, codex, gemini
  last_poll TEXT NOT NULL DEFAULT ''
);

-- Plain chats: talk to a model through the account gateway's OpenAI-compatible API, no tools, no folder.
-- The model is per message, so a conversation can switch models.
CREATE TABLE IF NOT EXISTS llm_chats (
  id         INTEGER PRIMARY KEY,
  title      TEXT NOT NULL DEFAULT '',
  model      TEXT NOT NULL DEFAULT '', -- the model the next reply uses
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS llm_chats_updated ON llm_chats (updated_at);

CREATE TABLE IF NOT EXISTS llm_messages (
  id         INTEGER PRIMARY KEY,
  chat_id    INTEGER NOT NULL,
  role       TEXT NOT NULL,              -- user | assistant
  model      TEXT NOT NULL DEFAULT '',   -- assistant: the model that answered
  content    TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'done', -- streaming | done | error
  error      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS llm_messages_chat ON llm_messages (chat_id, id);
