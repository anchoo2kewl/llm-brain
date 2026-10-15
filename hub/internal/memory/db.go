package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// The memories table (migration 0023). Rows are read and written only here.

const cols = `id, kind, title, body, repo, issue, pr, folder, files, agent, provider, host, session, confidence,
	supersedes, superseded_by, status, expires_at, hash, source, source_path, page_id, sync_state, attempts, next_try,
	last_error, created_at, updated_at`

func scan(sc interface{ Scan(...any) error }) (Memory, error) {
	var m Memory
	var files, exp, next, created, updated string
	err := sc.Scan(&m.ID, &m.Kind, &m.Title, &m.Body, &m.Scope.Repo, &m.Scope.Issue, &m.Scope.PR, &m.Scope.Folder, &files,
		&m.Author.Agent, &m.Author.Provider, &m.Author.Host, &m.Author.Session, &m.Confidence, &m.Supersedes, &m.Superseded,
		&m.Status, &exp, &m.Hash, &m.Source, &m.SourcePath, &m.PageID, &m.SyncState, &m.Attempts, &next, &m.LastError,
		&created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return m, store.ErrNotFound
	}
	_ = json.Unmarshal([]byte(files), &m.Scope.Files)
	m.ExpiresAt, m.NextTry, m.CreatedAt, m.UpdatedAt = store.ParseT(exp), store.ParseT(next), store.ParseT(created), store.ParseT(updated)
	return m, err
}

func list(ctx context.Context, db *sql.DB, q string, args ...any) ([]Memory, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+cols+` FROM memories `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func get(ctx context.Context, db *sql.DB, id int64) (Memory, error) {
	return scan(db.QueryRowContext(ctx, `SELECT `+cols+` FROM memories WHERE id = ?`, id))
}

// insert saves m and marks the memory it supersedes (m.Supersedes) in one transaction.
func insert(ctx context.Context, db *sql.DB, m *Memory) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	files, _ := json.Marshal(nonNil(m.Scope.Files))
	now := store.T(m.CreatedAt)
	res, err := tx.ExecContext(ctx, `INSERT INTO memories (kind, title, body, repo, issue, pr, folder, files, agent, provider, host,
		session, confidence, supersedes, status, expires_at, hash, source, source_path, sync_state, next_try, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Kind, m.Title, m.Body, m.Scope.Repo, m.Scope.Issue, m.Scope.PR, m.Scope.Folder, string(files), m.Author.Agent,
		m.Author.Provider, m.Author.Host, m.Author.Session, m.Confidence, m.Supersedes, store.T(m.ExpiresAt), m.Hash, m.Source,
		m.SourcePath, m.SyncState, now, now, now)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil || id == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT last_insert_rowid()`).Scan(&id); err != nil {
			return err
		}
	}
	m.ID = id
	if m.Supersedes > 0 {
		// The old page is rewritten as superseded on TaskAI too: back to pending, if it had synced.
		if _, err := tx.ExecContext(ctx, `UPDATE memories SET status = 'superseded', superseded_by = ?, updated_at = ?,
			sync_state = CASE WHEN sync_state = 'synced' THEN 'pending' ELSE sync_state END, next_try = ?
			WHERE id = ?`, id, now, now, m.Supersedes); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// due lists memories waiting to go to TaskAI.
func due(ctx context.Context, db *sql.DB, now time.Time, limit int) ([]Memory, error) {
	return list(ctx, db, `WHERE sync_state = 'pending' AND next_try <= ? ORDER BY id LIMIT ?`, store.T(now), limit)
}

func setSynced(ctx context.Context, db *sql.DB, id, pageID int64) error {
	_, err := db.ExecContext(ctx, `UPDATE memories SET sync_state = 'synced', page_id = ?, attempts = 0, last_error = '', updated_at = ?
		WHERE id = ?`, pageID, store.Now(), id)
	return err
}

// setPage records a page made before the content write failed, so a retry fills that page, not a new one.
func setPage(ctx context.Context, db *sql.DB, id, pageID int64) error {
	_, err := db.ExecContext(ctx, `UPDATE memories SET page_id = ? WHERE id = ?`, pageID, id)
	return err
}

func setRetry(ctx context.Context, db *sql.DB, id int64, attempts int, next time.Time, msg string) error {
	_, err := db.ExecContext(ctx, `UPDATE memories SET attempts = ?, next_try = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		attempts, store.T(next), msg, store.Now(), id)
	return err
}
