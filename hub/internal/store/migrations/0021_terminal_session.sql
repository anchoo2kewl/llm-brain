-- The CLI session a terminal runs (claude --session-id), so its transcript is matched exactly.
ALTER TABLE terminals ADD COLUMN session_uuid TEXT NOT NULL DEFAULT '';
