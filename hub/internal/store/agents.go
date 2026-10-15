package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Session control: agent sessions, the command queue, the prompt library and agent hosts. The rules (who may
// queue what, how a report moves a session) live in package agent; this file only reads and writes rows.

type AgentSession struct {
	ID              int64
	Host            string
	Cwd             string
	Provider        string
	Model           string
	Perm            string
	BypassOK        bool
	Title           string
	ProviderSession string
	Status          string
	PID             int
	ReviewOf        int64
	Adopted         bool
	Scratch         bool
	Turns           int
	CurrentCmd      int64 // the turn command whose reports move this session
	LastResult      string
	Error           string
	CreatedAt       string
	UpdatedAt       string
}

// Busy: a turn is queued or running, so another can't be queued.
func (a AgentSession) Busy() bool { return a.Status == "queued" || a.Status == "running" }

type Command struct {
	ID         int64           `json:"id"`
	Host       string          `json:"host"`
	Kind       string          `json:"kind"`
	AgentID    int64           `json:"agent_id"`
	Payload    json.RawMessage `json:"payload"`
	Status     string          `json:"status"`
	Result     string          `json:"result,omitempty"`
	CreatedAt  string          `json:"created_at"`
	ClaimedAt  string          `json:"claimed_at,omitempty"`
	FinishedAt string          `json:"finished_at,omitempty"`
}

type SavedPrompt struct {
	ID        int64
	Name      string
	Body      string
	UpdatedAt string
}

type AgentFolder struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
}

type AgentHost struct {
	Host      string
	Version   string
	Roots     []string
	Folders   []AgentFolder
	Providers []string
	LastPoll  string
}

// AgentLiveWindow: a host agent that polled this recently is live.
const AgentLiveWindow = 30 * time.Second

func (h AgentHost) Live() bool {
	return h.LastPoll != "" && h.LastPoll >= T(time.Now().Add(-AgentLiveWindow))
}

func insertID(ctx context.Context, tx *sql.Tx, q string, args ...any) (int64, error) {
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	if id, err := res.LastInsertId(); err == nil && id > 0 {
		return id, nil
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT last_insert_rowid()`).Scan(&id)
	return id, err
}

// ---------------------------------------------------------------- agent sessions

const agentCols = `id, host, cwd, provider, model, perm, bypass_ok, title, provider_session, status, pid, review_of, adopted, scratch,
	turns, current_cmd, last_result, error, created_at, updated_at`

func scanAgent(sc interface{ Scan(...any) error }) (AgentSession, error) {
	var a AgentSession
	var bypass, adopted, scratch int
	err := sc.Scan(&a.ID, &a.Host, &a.Cwd, &a.Provider, &a.Model, &a.Perm, &bypass, &a.Title, &a.ProviderSession, &a.Status,
		&a.PID, &a.ReviewOf, &adopted, &scratch, &a.Turns, &a.CurrentCmd, &a.LastResult, &a.Error, &a.CreatedAt, &a.UpdatedAt)
	a.BypassOK, a.Adopted, a.Scratch = bypass != 0, adopted != 0, scratch != 0
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return a, err
}

// CreateAgentSession inserts a and, when first is non-nil, queues it as the session's first command, in one
// transaction. It returns the session and command ids.
func (s *Store) CreateAgentSession(ctx context.Context, a AgentSession, first *Command) (int64, int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	now := Now()
	if a.Status == "" {
		a.Status = "idle"
	}
	id, err := insertID(ctx, tx, `INSERT INTO agent_sessions (host, cwd, provider, model, perm, bypass_ok, title, provider_session,
		status, review_of, adopted, scratch, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Host, a.Cwd, a.Provider, a.Model, a.Perm, b2i(a.BypassOK), a.Title, a.ProviderSession, a.Status, a.ReviewOf,
		b2i(a.Adopted), b2i(a.Scratch), now, now)
	if err != nil {
		return 0, 0, err
	}
	var cid int64
	if first != nil {
		first.AgentID = id
		if cid, err = queueTx(ctx, tx, *first); err != nil {
			return 0, 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET status = 'queued', turns = turns + 1, current_cmd = ? WHERE id = ?`, cid, id); err != nil {
			return 0, 0, err
		}
	}
	return id, cid, tx.Commit()
}

func (s *Store) AgentSession(ctx context.Context, id int64) (AgentSession, error) {
	return scanAgent(s.DB.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agent_sessions WHERE id = ?`, id))
}

// AgentSessionByProvider finds the agent session driving a CLI session on a host.
func (s *Store) AgentSessionByProvider(ctx context.Context, host, provider, providerSession string) (AgentSession, error) {
	return scanAgent(s.DB.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agent_sessions WHERE host = ? AND provider = ?
		AND provider_session = ? AND provider_session != ''`, host, provider, providerSession))
}

func (s *Store) AgentSessions(ctx context.Context, limit int) ([]AgentSession, error) {
	if limit <= 0 {
		limit = 300
	}
	return s.agentList(ctx, `SELECT `+agentCols+` FROM agent_sessions ORDER BY updated_at DESC LIMIT ?`, limit)
}

// SearchAgentSessions lists agent sessions whose title, folder, host or provider contains q.
func (s *Store) SearchAgentSessions(ctx context.Context, q string, limit int) ([]AgentSession, error) {
	if strings.TrimSpace(q) == "" {
		return s.AgentSessions(ctx, limit)
	}
	like := "%" + strings.NewReplacer("%", "", "_", "").Replace(strings.ToLower(strings.TrimSpace(q))) + "%"
	return s.agentList(ctx, `SELECT `+agentCols+` FROM agent_sessions WHERE lower(title) LIKE ? OR lower(cwd) LIKE ?
		OR lower(host) LIKE ? OR provider LIKE ? OR last_result LIKE ? ORDER BY updated_at DESC LIMIT ?`, like, like, like, like, like, limit)
}

// Reviews lists the review sessions of an agent session, newest first.
func (s *Store) Reviews(ctx context.Context, of int64) ([]AgentSession, error) {
	return s.agentList(ctx, `SELECT `+agentCols+` FROM agent_sessions WHERE review_of = ? ORDER BY id DESC`, of)
}

func (s *Store) agentList(ctx context.Context, q string, args ...any) ([]AgentSession, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentSession
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AgentUpdate is a partial update; empty / nil fields are left alone.
type AgentUpdate struct {
	Status          string
	PID             *int
	ProviderSession string
	Cwd             string
	LastResult      *string
	Error           *string
}

func (s *Store) UpdateAgentSession(ctx context.Context, id int64, u AgentUpdate) error {
	return updateAgent(ctx, s.DB, id, 0, u)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// updateAgent applies u; with onlyCmd > 0, only while that command is the session's current one.
func updateAgent(ctx context.Context, x execer, id, onlyCmd int64, u AgentUpdate) error {
	sets := []string{"updated_at = ?"}
	args := []any{Now()}
	add := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if u.Status != "" {
		add("status", u.Status)
	}
	if u.PID != nil {
		add("pid", *u.PID)
	}
	if u.ProviderSession != "" {
		add("provider_session", u.ProviderSession)
	}
	if u.Cwd != "" {
		add("cwd", u.Cwd)
	}
	if u.LastResult != nil {
		add("last_result", *u.LastResult)
	}
	if u.Error != nil {
		add("error", *u.Error)
	}
	q := `UPDATE agent_sessions SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
	args = append(args, id)
	if onlyCmd > 0 {
		q += ` AND current_cmd = ?`
		args = append(args, onlyCmd)
	}
	_, err := x.ExecContext(ctx, q, args...)
	return err
}

// FinalStatus: a command in one of these is finished and its record no longer changes.
func FinalStatus(s string) bool {
	switch s {
	case "done", "failed", "stopped", "cancelled":
		return true
	}
	return false
}

// ApplyReport records an executor's report on a command and moves its session, in one transaction. A
// command that already finished is left alone, so a retried or late report is a no-op; and a turn's report
// moves the session only while that turn is the session's current one, so an old turn can't overwrite a
// newer one. It returns the command and whether the report was applied.
func (s *Store) ApplyReport(ctx context.Context, id int64, status, result string, u AgentUpdate) (Command, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Command{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := scanCmd(tx.QueryRowContext(ctx, `SELECT `+cmdCols+` FROM commands WHERE id = ?`, id))
	if err != nil {
		return c, false, err
	}
	if FinalStatus(c.Status) {
		return c, false, nil
	}
	now := Now()
	fin := ""
	if FinalStatus(status) {
		fin = now
	}
	if _, err := tx.ExecContext(ctx, `UPDATE commands SET status = ?, result = ?, updated_at = ?,
		finished_at = CASE WHEN ? != '' THEN ? ELSE finished_at END WHERE id = ?`, status, result, now, fin, fin, id); err != nil {
		return c, false, err
	}
	if c.Kind == "turn" && c.AgentID > 0 {
		if u.ProviderSession != "" { // never take another session's CLI id (the unique index would refuse it)
			var other int64
			err := tx.QueryRowContext(ctx, `SELECT a.id FROM agent_sessions a, agent_sessions me WHERE me.id = ? AND a.id != me.id
				AND a.host = me.host AND a.provider = me.provider AND a.provider_session = ?`, c.AgentID, u.ProviderSession).Scan(&other)
			if err == nil {
				u.ProviderSession = ""
			} else if !errors.Is(err, sql.ErrNoRows) {
				return c, false, err
			}
		}
		if err := updateAgent(ctx, tx, c.AgentID, c.ID, u); err != nil {
			return c, false, err
		}
	}
	c.Status = status
	return c, true, tx.Commit()
}

// AdoptAgentSession returns the agent session for a's CLI session (host, provider, provider_session),
// creating it as a when there is none, atomically.
func (s *Store) AdoptAgentSession(ctx context.Context, a AgentSession) (int64, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM agent_sessions WHERE host = ? AND provider = ? AND provider_session = ?`,
		a.Host, a.Provider, a.ProviderSession).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	now := Now()
	id, err = insertID(ctx, tx, `INSERT INTO agent_sessions (host, cwd, provider, model, perm, bypass_ok, title, provider_session,
		status, adopted, scratch, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'idle', 1, ?, ?, ?)`,
		a.Host, a.Cwd, a.Provider, a.Model, a.Perm, b2i(a.BypassOK), a.Title, a.ProviderSession, b2i(a.Scratch), now, now)
	if err != nil {
		return 0, false, err
	}
	return id, true, tx.Commit()
}

// QueueTurn queues a command for an agent session and marks it queued, unless a turn is already queued or
// running (ErrBusy).
func (s *Store) QueueTurn(ctx context.Context, c Command) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM agent_sessions WHERE id = ?`, c.AgentID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if status == "queued" || status == "running" {
		return 0, ErrBusy
	}
	id, err := queueTx(ctx, tx, c)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET status = 'queued', turns = turns + 1, error = '', current_cmd = ?,
		updated_at = ? WHERE id = ?`, id, Now(), c.AgentID); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

var ErrBusy = errors.New("a turn is already queued or running")

// ---------------------------------------------------------------- commands

func queueTx(ctx context.Context, tx *sql.Tx, c Command) (int64, error) {
	if len(c.Payload) == 0 {
		c.Payload = json.RawMessage("{}")
	}
	now := Now()
	return insertID(ctx, tx, `INSERT INTO commands (host, kind, agent_id, payload, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'queued', ?, ?)`, c.Host, c.Kind, c.AgentID, string(c.Payload), now, now)
}

// QueueCommand queues a command that doesn't start a turn (stop).
func (s *Store) QueueCommand(ctx context.Context, c Command) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := queueTx(ctx, tx, c)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

const cmdCols = `id, host, kind, agent_id, payload, status, result, created_at, claimed_at, finished_at`

func scanCmd(sc interface{ Scan(...any) error }) (Command, error) {
	var c Command
	var payload string
	err := sc.Scan(&c.ID, &c.Host, &c.Kind, &c.AgentID, &payload, &c.Status, &c.Result, &c.CreatedAt, &c.ClaimedAt, &c.FinishedAt)
	c.Payload = json.RawMessage(payload)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return c, err
}

func (s *Store) Command(ctx context.Context, id int64) (Command, error) {
	return scanCmd(s.DB.QueryRowContext(ctx, `SELECT `+cmdCols+` FROM commands WHERE id = ?`, id))
}

func (s *Store) cmdList(ctx context.Context, q string, args ...any) ([]Command, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Command
	for rows.Next() {
		c, err := scanCmd(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Commands lists recent commands, newest first; agentID > 0 limits them to one agent session.
func (s *Store) Commands(ctx context.Context, agentID int64, limit int) ([]Command, error) {
	if agentID > 0 {
		return s.cmdList(ctx, `SELECT `+cmdCols+` FROM commands WHERE agent_id = ? ORDER BY id DESC LIMIT ?`, agentID, limit)
	}
	return s.cmdList(ctx, `SELECT `+cmdCols+` FROM commands ORDER BY id DESC LIMIT ?`, limit)
}

// ClaimCommands hands a host its queued commands, oldest first, and marks them claimed.
func (s *Store) ClaimCommands(ctx context.Context, host string) ([]Command, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT `+cmdCols+` FROM commands WHERE host = ? AND status = 'queued' ORDER BY id LIMIT 20`, host)
	if err != nil {
		return nil, err
	}
	var out []Command
	for rows.Next() {
		c, err := scanCmd(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	now := Now()
	for i := range out {
		if _, err := tx.ExecContext(ctx, `UPDATE commands SET status = 'claimed', claimed_at = ?, updated_at = ? WHERE id = ?`,
			now, now, out[i].ID); err != nil {
			return nil, err
		}
		out[i].Status, out[i].ClaimedAt = "claimed", now
	}
	return out, tx.Commit()
}

// InFlight lists a host's claimed or running commands.
func (s *Store) InFlight(ctx context.Context, host string) ([]Command, error) {
	return s.cmdList(ctx, `SELECT `+cmdCols+` FROM commands WHERE host = ? AND status IN ('claimed', 'running') ORDER BY id`, host)
}

// FinishCommand sets a command's status and last report; a final status also stamps finished_at.
func (s *Store) FinishCommand(ctx context.Context, id int64, status, result string) error {
	now := Now()
	fin := ""
	switch status {
	case "done", "failed", "stopped", "cancelled":
		fin = now
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE commands SET status = ?, result = ?, updated_at = ?,
		finished_at = CASE WHEN ? != '' THEN ? ELSE finished_at END
		WHERE id = ? AND status NOT IN ('done', 'failed', 'stopped', 'cancelled')`, status, result, now, fin, fin, id)
	return err
}

// CancelQueued cancels an agent session's commands that no host has claimed yet; it returns how many.
func (s *Store) CancelQueued(ctx context.Context, agentID int64) (int, error) {
	now := Now()
	res, err := s.DB.ExecContext(ctx, `UPDATE commands SET status = 'cancelled', finished_at = ?, updated_at = ?
		WHERE agent_id = ? AND status = 'queued' AND kind = 'turn'`, now, now, agentID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ---------------------------------------------------------------- prompt library

func (s *Store) SavedPrompts(ctx context.Context) ([]SavedPrompt, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, body, updated_at FROM saved_prompts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedPrompt
	for rows.Next() {
		var p SavedPrompt
		if err := rows.Scan(&p.ID, &p.Name, &p.Body, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SavedPrompt(ctx context.Context, id int64) (SavedPrompt, error) {
	var p SavedPrompt
	err := s.DB.QueryRowContext(ctx, `SELECT id, name, body, updated_at FROM saved_prompts WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Body, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return p, err
}

// SavePrompt creates (id 0) or updates a saved prompt.
func (s *Store) SavePrompt(ctx context.Context, p SavedPrompt) (int64, error) {
	now := Now()
	if p.ID > 0 {
		res, err := s.DB.ExecContext(ctx, `UPDATE saved_prompts SET name = ?, body = ?, updated_at = ? WHERE id = ?`, p.Name, p.Body, now, p.ID)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, ErrNotFound
		}
		return p.ID, nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := insertID(ctx, tx, `INSERT INTO saved_prompts (name, body, created_at, updated_at) VALUES (?, ?, ?, ?)`, p.Name, p.Body, now, now)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) DeletePrompt(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM saved_prompts WHERE id = ?`, id)
	return err
}

// ---------------------------------------------------------------- agent hosts

// TouchAgentHost records a poll.
func (s *Store) TouchAgentHost(ctx context.Context, host, version string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO agent_hosts (host, version, last_poll) VALUES (?, ?, ?)
		ON CONFLICT (host) DO UPDATE SET version = excluded.version, last_poll = excluded.last_poll`, host, version, Now())
	return err
}

// SetAgentInventory records a host's roots and folders.
func (s *Store) SetAgentInventory(ctx context.Context, host, version string, roots []string, folders []AgentFolder, providers []string) error {
	r, _ := json.Marshal(roots)
	f, _ := json.Marshal(folders)
	p, _ := json.Marshal(providers)
	if providers == nil {
		p = []byte("[]")
	}
	if roots == nil {
		r = []byte("[]")
	}
	if folders == nil {
		f = []byte("[]")
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO agent_hosts (host, version, roots, folders, providers, last_poll) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (host) DO UPDATE SET version = excluded.version, roots = excluded.roots, folders = excluded.folders,
		providers = excluded.providers, last_poll = excluded.last_poll`, host, version, string(r), string(f), string(p), Now())
	return err
}

func (s *Store) AgentHosts(ctx context.Context) ([]AgentHost, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT host, version, roots, folders, providers, last_poll FROM agent_hosts ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentHost
	for rows.Next() {
		var h AgentHost
		var roots, folders, provs string
		if err := rows.Scan(&h.Host, &h.Version, &roots, &folders, &provs, &h.LastPoll); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(roots), &h.Roots)
		_ = json.Unmarshal([]byte(folders), &h.Folders)
		_ = json.Unmarshal([]byte(provs), &h.Providers)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) AgentHost(ctx context.Context, host string) (AgentHost, error) {
	hs, err := s.AgentHosts(ctx)
	if err != nil {
		return AgentHost{}, err
	}
	for _, h := range hs {
		if h.Host == host {
			return h, nil
		}
	}
	return AgentHost{}, ErrNotFound
}

// LatestRunByKey is the newest run with this key on a host (an agent turn's log), 0 when there is none.
func (s *Store) LatestRunByKey(ctx context.Context, host, key string) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM runs WHERE host = ? AND key = ? ORDER BY started_at DESC LIMIT 1`, host, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}
