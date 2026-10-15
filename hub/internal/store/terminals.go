package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Terminal is an interactive CLI running in tmux on a host (migration 0015).
type Terminal struct {
	ID        int64
	Host      string
	Provider  string
	Cwd       string
	Model     string
	Title     string
	Status    string // starting | live | ended | failed
	Error     string
	CreatedAt string
	UpdatedAt string
	LiveAt    string
	EndedAt   string
}

func (t Terminal) Open() bool { return t.Status == "starting" || t.Status == "live" }

const termCols = `id, host, provider, cwd, model, title, status, error, created_at, updated_at, live_at, ended_at`

func scanTerm(sc interface{ Scan(...any) error }) (Terminal, error) {
	var t Terminal
	err := sc.Scan(&t.ID, &t.Host, &t.Provider, &t.Cwd, &t.Model, &t.Title, &t.Status, &t.Error, &t.CreatedAt, &t.UpdatedAt, &t.LiveAt, &t.EndedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return t, err
}

// CreateTerminal adds a terminal and queues its open command, in one transaction.
func (s *Store) CreateTerminal(ctx context.Context, t Terminal, open Command) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	now := Now()
	id, err := insertID(ctx, tx, `INSERT INTO terminals (host, provider, cwd, model, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'starting', ?, ?)`, t.Host, t.Provider, t.Cwd, t.Model, t.Title, now, now)
	if err != nil {
		return 0, err
	}
	open.AgentID = id
	if _, err := queueTx(ctx, tx, open); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) Terminal(ctx context.Context, id int64) (Terminal, error) {
	return scanTerm(s.DB.QueryRowContext(ctx, `SELECT `+termCols+` FROM terminals WHERE id = ?`, id))
}

// Terminals lists terminals, newest activity first; q matches title, folder or provider.
func (s *Store) Terminals(ctx context.Context, q string, limit int) ([]Terminal, error) {
	query, args := `SELECT `+termCols+` FROM terminals`, []any{}
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + strings.ToLower(q) + "%"
		query += ` WHERE lower(title) LIKE ? OR lower(cwd) LIKE ? OR provider LIKE ?`
		args = append(args, like, like, like)
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Terminal
	for rows.Next() {
		t, err := scanTerm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetTerminal moves a terminal. An ended or failed terminal stays that way; cwd is set when given.
func (s *Store) SetTerminal(ctx context.Context, id int64, status, cwd, errMsg string) error {
	return setTerminal(ctx, s.DB, id, status, cwd, errMsg)
}

func setTerminal(ctx context.Context, x execer, id int64, status, cwd, errMsg string) error {
	now := Now()
	ended, live := "", ""
	switch status {
	case "ended", "failed":
		ended = now
	case "live":
		live = now
	}
	_, err := x.ExecContext(ctx, `UPDATE terminals SET status = ?, error = ?, updated_at = ?,
		cwd = CASE WHEN ? != '' THEN ? ELSE cwd END,
		live_at = CASE WHEN ? != '' THEN ? ELSE live_at END,
		ended_at = CASE WHEN ? != '' THEN ? ELSE ended_at END
		WHERE id = ? AND status NOT IN ('ended', 'failed')`, status, errMsg, now, cwd, cwd, live, live, ended, ended, id)
	return err
}

// ApplyTermReport finishes a terminal command and, only when this report is the one that finished it, moves
// its terminal (tstatus ” = leave it), in one transaction. A retried report finds the command final and
// changes nothing, so the terminal can't be left behind its command.
func (s *Store) ApplyTermReport(ctx context.Context, cmdID int64, status, result string, termID int64, tstatus, cwd, errMsg string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := Now()
	fin := ""
	if FinalStatus(status) {
		fin = now
	}
	res, err := tx.ExecContext(ctx, `UPDATE commands SET status = ?, result = ?, updated_at = ?,
		finished_at = CASE WHEN ? != '' THEN ? ELSE finished_at END
		WHERE id = ? AND status NOT IN ('done', 'failed', 'stopped', 'cancelled')`, status, result, now, fin, fin, cmdID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 && tstatus != "" {
		if err := setTerminal(ctx, tx, termID, tstatus, cwd, errMsg); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CancelQueuedTerm cancels a terminal's commands no host has claimed yet; opened says its term_open was one.
func (s *Store) CancelQueuedTerm(ctx context.Context, termID int64) (opened bool, err error) {
	now := Now()
	res, err := s.DB.ExecContext(ctx, `UPDATE commands SET status = 'cancelled', updated_at = ?, finished_at = ?
		WHERE agent_id = ? AND kind = 'term_open' AND status = 'queued'`, now, now, termID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	_, err = s.DB.ExecContext(ctx, `UPDATE commands SET status = 'cancelled', updated_at = ?, finished_at = ?
		WHERE agent_id = ? AND kind LIKE 'term\_%' ESCAPE '\' AND status = 'queued'`, now, now, termID)
	return n > 0, err
}

// TouchTerminal bumps updated_at (someone attached), so the list shows recent use first.
func (s *Store) TouchTerminal(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE terminals SET updated_at = ? WHERE id = ?`, Now(), id)
	return err
}

// TermSeen is a terminal a host's tmux has right now, with the folder it started in.
type TermSeen struct {
	ID  int64  `json:"id"`
	Cwd string `json:"cwd,omitempty"`
}

// SyncTerminals squares the host's terminals with a tmux listing taken at or after since:
//   - live ones missing from it end, unless they became live after since (the listing predates them);
//   - ones it has that the hub thinks are starting, ended or failed are live again (an open whose report
//     was lost, or a restart's lost-command check), unless they ended after since (a kill the listing predates).
func (s *Store) SyncTerminals(ctx context.Context, host string, seen []TermSeen, since string) (ended, revived int, err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	now := Now()
	have := map[int64]bool{}
	for _, t := range seen {
		have[t.ID] = true
		// The predicates re-check the state in the write itself, so a report that lands meanwhile wins.
		res, err := tx.ExecContext(ctx, `UPDATE terminals SET status = 'live', error = '', updated_at = ?, live_at = ?, ended_at = '',
			cwd = CASE WHEN ? != '' THEN ? ELSE cwd END
			WHERE id = ? AND host = ? AND status != 'live' AND (ended_at = '' OR ended_at < ?)`, now, now, t.Cwd, t.Cwd, t.ID, host, since)
		if err != nil {
			return 0, 0, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			revived++
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM terminals WHERE host = ? AND status = 'live' AND live_at < ?`, host, since)
	if err != nil {
		return 0, 0, err
	}
	var gone []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, err
		}
		if !have[id] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	for _, id := range gone {
		res, err := tx.ExecContext(ctx, `UPDATE terminals SET status = 'ended', updated_at = ?, ended_at = ?
			WHERE id = ? AND status = 'live' AND live_at < ?`, now, now, id, since)
		if err != nil {
			return 0, 0, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			ended++
		}
	}
	return ended, revived, tx.Commit()
}

// SetAgentPlaces records a host's home folder and the folders directly in it.
func (s *Store) SetAgentPlaces(ctx context.Context, host, home string, places []string) error {
	if places == nil {
		places = []string{}
	}
	b, _ := json.Marshal(places)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO agent_places (host, home, places) VALUES (?, ?, ?)
		ON CONFLICT (host) DO UPDATE SET home = excluded.home, places = excluded.places`, host, home, string(b))
	return err
}

// AgentPlaces is a host's home and the folders in it ('' and nil before its agent reports them).
func (s *Store) AgentPlaces(ctx context.Context, host string) (string, []string, error) {
	var home, places string
	err := s.DB.QueryRowContext(ctx, `SELECT home, places FROM agent_places WHERE host = ?`, host).Scan(&home, &places)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	var out []string
	_ = json.Unmarshal([]byte(places), &out)
	return home, out, err
}

// RecentFolders are the folders under home where the host's Claude/Codex sessions and terminals ran lately,
// most recent first.
func (s *Store) RecentFolders(ctx context.Context, host, home string, limit int) ([]string, error) {
	if home == "" {
		return nil, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT cwd, max(at) AS last FROM (
			SELECT cwd, file_mtime AS at FROM sessions WHERE host = ? AND cwd LIKE ? AND file_mtime > ?
			UNION ALL SELECT cwd, updated_at AS at FROM terminals WHERE host = ? AND cwd LIKE ?)
		GROUP BY cwd ORDER BY last DESC LIMIT ?`,
		host, home+"/%", T(time.Now().AddDate(0, 0, -30)), host, home+"/%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cwd, last string
		if err := rows.Scan(&cwd, &last); err != nil {
			return nil, err
		}
		out = append(out, cwd)
	}
	return out, rows.Err()
}
