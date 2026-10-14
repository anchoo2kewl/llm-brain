-- Account gateway (CLIProxyAPI): one row per logged-in account, refreshed by the poller.
CREATE TABLE IF NOT EXISTS gateway_accounts (
 id TEXT PRIMARY KEY,          -- credential file name, e.g. claude-31399bd6-<email>.json
 auth_index TEXT NOT NULL,     -- the gateway's id for this account (matches gateway_requests.auth_index)
 provider TEXT NOT NULL,       -- claude | codex | ...
 email TEXT NOT NULL,
 label TEXT NOT NULL,          -- personal | work | ''
 priority INTEGER NOT NULL,    -- higher wins (fill-first)
 disabled INTEGER NOT NULL,
 failed INTEGER NOT NULL,
 cooldowns TEXT NOT NULL,      -- JSON array as reported
 windows TEXT NOT NULL,        -- JSON []Window (normalized quota windows)
 quota_at TEXT NOT NULL,       -- when the gateway last saw quota headers for it ('' if never)
 seen_at TEXT NOT NULL         -- last poll that listed it
);

-- One row per request the gateway served (drained from its usage queue).
CREATE TABLE IF NOT EXISTS gateway_requests (
 request_id TEXT PRIMARY KEY,  -- gateway request id (the queue is drained on read, so this dedupes re-polls)
 ts TEXT NOT NULL,
 auth_index TEXT NOT NULL,
 account TEXT NOT NULL,        -- email the gateway reported as source
 model TEXT NOT NULL,
 input_tokens INTEGER NOT NULL,
 output_tokens INTEGER NOT NULL,
 cached_tokens INTEGER NOT NULL,
 total_tokens INTEGER NOT NULL,
 latency_ms INTEGER NOT NULL,
 failed INTEGER NOT NULL,
 status_code INTEGER NOT NULL,
 user_agent TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS gateway_requests_ts ON gateway_requests (ts);
