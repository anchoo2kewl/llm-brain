-- Insights: small derived tables the /insights page aggregates over, so charts never scan the ~GB messages
-- table. A background rollup (internal/insights) fills them from messages in id order and remembers how far
-- it got; they can be dropped and rebuilt at any time (delete the cursor row).

CREATE TABLE IF NOT EXISTS insights_cursor (
  name    TEXT PRIMARY KEY,
  last_id INTEGER NOT NULL
);

-- One row per model response with token usage (Claude transcripts carry usage per response; a response
-- spans several lines with the same API message id, so (session_id, api_id) dedupes them).
CREATE TABLE IF NOT EXISTS usage_events (
  session_id  INTEGER NOT NULL,
  api_id      TEXT NOT NULL,
  ts          TEXT NOT NULL,
  model       TEXT NOT NULL,
  input       INTEGER NOT NULL,
  output      INTEGER NOT NULL,
  cache_read  INTEGER NOT NULL,
  cache_write INTEGER NOT NULL,
  PRIMARY KEY (session_id, api_id)
);
CREATE INDEX IF NOT EXISTS usage_events_ts ON usage_events (ts);

-- One row per tool call; is_error is set when its result comes back as an error.
CREATE TABLE IF NOT EXISTS tool_calls (
  message_id INTEGER PRIMARY KEY,
  session_id INTEGER NOT NULL,
  ts         TEXT NOT NULL,
  tool       TEXT NOT NULL,
  use_id     TEXT NOT NULL,
  is_error   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS tool_calls_ts ON tool_calls (ts);
CREATE INDEX IF NOT EXISTS tool_calls_use ON tool_calls (session_id, use_id);

-- Messages per session per UTC hour ("2026-10-14T20"): blocks = every stored block, prompts = text a person
-- typed (user, not injected context).
CREATE TABLE IF NOT EXISTS activity (
  hour       TEXT NOT NULL,
  session_id INTEGER NOT NULL,
  blocks     INTEGER NOT NULL DEFAULT 0,
  prompts    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (hour, session_id)
);
CREATE INDEX IF NOT EXISTS activity_session ON activity (session_id);

-- Gateway quota windows over time (the accounts table holds only the latest view). A row is written when
-- a window's used % or reset changes, or every 10 minutes.
CREATE TABLE IF NOT EXISTS gateway_quota_history (
  account_id TEXT NOT NULL,
  win        TEXT NOT NULL,       -- 5h | 7d | primary | ...
  at         TEXT NOT NULL,
  used_pct   REAL NOT NULL,
  reset_at   INTEGER NOT NULL,
  PRIMARY KEY (account_id, win, at)
);
CREATE INDEX IF NOT EXISTS gateway_quota_history_at ON gateway_quota_history (at);

CREATE INDEX IF NOT EXISTS sessions_first ON sessions (first_at);
CREATE INDEX IF NOT EXISTS gateway_requests_auth ON gateway_requests (auth_index, ts);
