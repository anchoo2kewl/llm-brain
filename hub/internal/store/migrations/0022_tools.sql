-- The CLIs each host agent can update (brew, claude, codex, gemini, ...): version and how it was installed.
ALTER TABLE agent_hosts ADD COLUMN tools TEXT NOT NULL DEFAULT '[]';
