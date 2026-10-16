-- Closing a session from the hub: the session_close command last queued for it, and when its process was
-- ended. A session counts as closed while nothing was written after closed_at.
ALTER TABLE sessions ADD COLUMN close_cmd INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN closed_at TEXT NOT NULL DEFAULT '';
