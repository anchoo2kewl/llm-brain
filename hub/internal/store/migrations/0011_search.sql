-- Rows the search index must re-read. Sessions and runs change in place (titles, logs, status), so the
-- ingest transaction that changes one records it here; the indexer re-indexes it and deletes the row only
-- if gen hasn't moved since it read it. (Messages and events are append-only: the index tracks them by id.)
CREATE TABLE IF NOT EXISTS search_dirty (
  kind TEXT NOT NULL,                 -- s (session) | r (run)
  id   INTEGER NOT NULL,
  gen  INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (kind, id)
);
