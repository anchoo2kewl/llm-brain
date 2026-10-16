-- A hub terminal whose CLI is frozen waiting on something nobody can answer from the hub (a macOS privacy
-- prompt on the Mac's own screen): the host agent's watchdog reports it with each inventory, as JSON
-- (store.TermBlock); '' when it isn't blocked.
ALTER TABLE terminals ADD COLUMN blocked TEXT NOT NULL DEFAULT '';
