-- Where a host's terminals may start, for the folder picker: the host's home and the folders directly in it.
-- (Recent folders come from the sessions table.)
CREATE TABLE IF NOT EXISTS agent_places (
  host   TEXT PRIMARY KEY,
  home   TEXT NOT NULL DEFAULT '',
  places TEXT NOT NULL DEFAULT '[]'
);
