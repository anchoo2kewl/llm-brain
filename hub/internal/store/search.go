package store

import (
	"context"
	"database/sql"
	"strings"
)

// Rows for the search index: read in id order, a page at a time, so the single connection is never held for
// long. And lookups by id, to turn index hits into results.

// MarkDirty records that a session ("s"), run ("r") or chat message ("c") changed and must be re-indexed. Call it inside the
// transaction that changed the row.
func MarkDirty(ctx context.Context, x interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, kind string, id int64) error {
	_, err := x.ExecContext(ctx, `INSERT INTO search_dirty (kind, id, gen) VALUES (?, ?, 1)
		ON CONFLICT (kind, id) DO UPDATE SET gen = gen + 1`, kind, id)
	return err
}

type Dirty struct {
	Kind string
	ID   int64
	Gen  int64
}

func (s *Store) DirtyRows(ctx context.Context, limit int) ([]Dirty, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT kind, id, gen FROM search_dirty ORDER BY kind, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Dirty
	for rows.Next() {
		var d Dirty
		if err := rows.Scan(&d.Kind, &d.ID, &d.Gen); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ClearDirty deletes the rows that haven't changed again since they were read.
func (s *Store) ClearDirty(ctx context.Context, ds []Dirty) error {
	if len(ds) == 0 {
		return nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, d := range ds {
		if _, err := tx.ExecContext(ctx, `DELETE FROM search_dirty WHERE kind = ? AND id = ? AND gen = ?`, d.Kind, d.ID, d.Gen); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// IndexMessage is a message row with the session fields the index filters on.
type IndexMessage struct {
	ID, SessionID                     int64
	Role, Kind, TS, Text, Tool, Input string
	Host, Provider, SessionUUID       string
}

// MessagesAfter pages messages in id order.
func (s *Store) MessagesAfter(ctx context.Context, after int64, limit int) ([]IndexMessage, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id, m.session_id, m.role, m.kind, m.ts, m.text, m.tool_name, m.tool_input,
		COALESCE(s.host, ''), COALESCE(s.provider, ''), COALESCE(s.session_uuid, '')
		FROM messages m LEFT JOIN sessions s ON s.id = m.session_id WHERE m.id > ? ORDER BY m.id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexMessage
	for rows.Next() {
		var m IndexMessage
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Kind, &m.TS, &m.Text, &m.Tool, &m.Input, &m.Host, &m.Provider, &m.SessionUUID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MaxID is the highest id in a table, for progress.
func (s *Store) MaxID(ctx context.Context, table string) (int64, error) {
	switch table {
	case "messages", "events", "sessions", "runs", "chat_messages":
	default:
		return 0, sql.ErrNoRows
	}
	var n int64
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(max(id), 0) FROM `+table).Scan(&n)
	return n, err
}

type IndexEvent struct {
	ID                          int64
	Host, Path, TS, Level, Text string
}

func (s *Store) EventsAfter(ctx context.Context, after int64, limit int) ([]IndexEvent, error) {
	return s.queryEvents(ctx, `SELECT id, host, path, ts, level, text FROM events WHERE id > ? ORDER BY id LIMIT ?`, after, limit)
}

func (s *Store) EventsByID(ctx context.Context, ids []int64) ([]IndexEvent, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q, args := inList(`SELECT id, host, path, ts, level, text FROM events WHERE id IN `, ids)
	return s.queryEvents(ctx, q, args...)
}

func (s *Store) queryEvents(ctx context.Context, q string, args ...any) ([]IndexEvent, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexEvent
	for rows.Next() {
		var e IndexEvent
		if err := rows.Scan(&e.ID, &e.Host, &e.Path, &e.TS, &e.Level, &e.Text); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SessionsAfter pages sessions in id order; SessionsByID looks them up.
func (s *Store) SessionsAfter(ctx context.Context, after int64, limit int) ([]Session, error) {
	return s.querySessions(ctx, `SELECT `+sessCols+` FROM sessions s WHERE s.id > ? ORDER BY s.id LIMIT ?`, after, limit)
}

func (s *Store) SessionsByID(ctx context.Context, ids []int64) ([]Session, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q, args := inList(`SELECT `+sessCols+` FROM sessions s WHERE s.id IN `, ids)
	return s.querySessions(ctx, q, args...)
}

// RunsAfter pages runs in id order, with log and prompt; RunsByID looks them up.
func (s *Store) RunsAfter(ctx context.Context, after int64, limit int) ([]Run, error) {
	return s.queryFullRuns(ctx, `SELECT `+runCols+`, r.log, r.prompt FROM runs r LEFT JOIN hosts h ON h.host = r.host
		WHERE r.id > ? ORDER BY r.id LIMIT ?`, after, limit)
}

func (s *Store) RunsByID(ctx context.Context, ids []int64) ([]Run, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q, args := inList(`SELECT `+runCols+`, r.log, r.prompt FROM runs r LEFT JOIN hosts h ON h.host = r.host WHERE r.id IN `, ids)
	return s.queryFullRuns(ctx, q, args...)
}

func (s *Store) queryFullRuns(ctx context.Context, q string, args ...any) ([]Run, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var seen string
		if err := rows.Scan(&r.ID, &r.Host, &r.Path, &r.Key, &r.Kind, &r.Target, &r.Provider, &r.StartedAt, &r.EndedAt,
			&r.Status, &r.Summary, &r.LogBytes, &r.SessionID, &seen, &r.Log, &r.Prompt); err != nil {
			return nil, err
		}
		r.HostLive = seen >= ago(HostWindow)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MessagesByID returns hits (message plus its session) for the given message ids.
func (s *Store) MessagesByID(ctx context.Context, ids []int64) ([]Hit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q, args := inList(`SELECT m.id, m.session_id, m.role, m.kind, m.meta, m.ts, m.text, m.tool_name, m.tool_use_id, m.tool_input, m.is_error,
		`+sessCols+` FROM messages m JOIN sessions s ON s.id = m.session_id WHERE m.id IN `, ids)
	return s.queryHits(ctx, q, args...)
}

func inList(prefix string, ids []int64) (string, []any) {
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString("(")
	args := make([]any, len(ids))
	for i, id := range ids {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("?")
		args[i] = id
	}
	b.WriteString(")")
	return b.String(), args
}
