-- Inbox: what the person has already seen or dismissed, by item key ("s:<session>", "a:<task>", "c:<conv>", ...),
-- with the time of the newest thing seen; an item comes back when something newer happens.
CREATE TABLE IF NOT EXISTS inbox_seen (
  key TEXT PRIMARY KEY,
  at  TEXT NOT NULL
);

-- Web Push subscriptions (one per browser that turned notifications on).
CREATE TABLE IF NOT EXISTS push_subs (
  endpoint   TEXT PRIMARY KEY,
  p256dh     TEXT NOT NULL,
  auth       TEXT NOT NULL,
  label      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  last_ok    TEXT NOT NULL DEFAULT '',
  last_err   TEXT NOT NULL DEFAULT ''
);
