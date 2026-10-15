-- Hub settings, one row per key. The Slack token lives here (slack_token); it is never rendered, logged or
-- returned by an API.
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- Chat messages read from Slack (read-only). A message is stored once, under its real conversation: a
-- mention of me found by search.messages and the same message read from its channel are one row, with
-- mentions_me set.
CREATE TABLE IF NOT EXISTS chat_messages (
  id          INTEGER PRIMARY KEY,            -- rows are never deleted, so ids only grow
  source      TEXT NOT NULL,              -- slack
  conv_id     TEXT NOT NULL,              -- C… / G… / D…
  conv_name   TEXT NOT NULL,              -- channel name, or the other person for a DM
  conv_kind   TEXT NOT NULL,              -- channel | private | dm | group_dm
  mentions_me INTEGER NOT NULL DEFAULT 0,
  ts          TEXT NOT NULL,              -- Slack message ts
  thread_ts   TEXT NOT NULL DEFAULT '',   -- parent ts for a thread reply or parent, '' otherwise
  user_id     TEXT NOT NULL DEFAULT '',
  user_name   TEXT NOT NULL DEFAULT '',
  text        TEXT NOT NULL,              -- cleaned
  permalink   TEXT NOT NULL DEFAULT '',
  at          TEXT NOT NULL,              -- ts as a store time
  UNIQUE (conv_id, ts)
);
CREATE INDEX IF NOT EXISTS chat_messages_at ON chat_messages (at);
CREATE INDEX IF NOT EXISTS chat_messages_conv ON chat_messages (conv_id, at);

-- How far each conversation has been read: the newest ts read (the next history call's `oldest`), and for
-- each thread parent, the newest reply ts read (JSON object parent ts -> reply ts).
CREATE TABLE IF NOT EXISTS chat_cursors (
  conv_id TEXT PRIMARY KEY,
  oldest  TEXT NOT NULL DEFAULT '',
  threads TEXT NOT NULL DEFAULT '{}'
);

-- Display names of Slack users (users.info), so a restart doesn't look everyone up again.
CREATE TABLE IF NOT EXISTS chat_users (
  user_id TEXT PRIMARY KEY,
  name    TEXT NOT NULL
);
