package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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
	EndedAt   string
}

func (t Terminal) Open() bool { return t.Status == "starting" || t.Status == "live" }

const termCols = `id, host, provider, cwd, model, title, status, error, created_at, updated_at, ended_at`

func scanTerm(sc interface{ Scan(...any) error }) (Terminal, error) {
	var t Terminal
	err := sc.Scan(&t.ID, &t.Host, &t.Provider, &t.Cwd, &t.Model, &t.Title, &t.Status, &t.Error, &t.CreatedAt, &t.UpdatedAt, &t.EndedAt)
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
	now := Now()
	ended := ""
	if status == "ended" || status == "failed" {
		ended = now
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE terminals SET status = ?, error = ?, updated_at = ?,
		cwd = CASE WHEN ? != '' THEN ? ELSE cwd END,
		ended_at = CASE WHEN ? != '' THEN ? ELSE ended_at END
		WHERE id = ? AND status NOT IN ('ended', 'failed')`, status, errMsg, now, cwd, cwd, ended, ended, id)
	return err
}

// TouchTerminal bumps updated_at (someone attached), so the list shows recent use first.
func (s *Store) TouchTerminal(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE terminals SET updated_at = ? WHERE id = ?`, Now(), id)
	return err
}

// EndMissingTerminals ends the host's live terminals whose tmux session is gone. alive holds terminal ids.
// Terminals still starting are left to their open command.
func (s *Store) EndMissingTerminals(ctx context.Context, host string, alive []int64) (int, error) {
	have := map[int64]bool{}
	for _, id := range alive {
		have[id] = true
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM terminals WHERE host = ? AND status = 'live'`, host)
	if err != nil {
		return 0, err
	}
	var gone []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		if !have[id] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	for _, id := range gone {
		if err := s.SetTerminal(ctx, id, "ended", "", ""); err != nil {
			return 0, err
		}
	}
	return len(gone), nil
}
