package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/parse"
)

var ErrNotFound = errors.New("not found")

type Run struct {
	ID        int64
	Host      string
	Path      string
	Key       string
	Kind      string
	Target    string
	Provider  string
	StartedAt string
	EndedAt   string
	Status    string
	Summary   string
	LogBytes  int64
	Log       string
	Prompt    string
	SessionID int64
	HostLive  bool // the run's host has checked in recently, so "running" is current
}

type Session struct {
	ID           int64
	Host         string
	Path         string
	Provider     string
	UUID         string
	ParentUUID   string
	Project      string
	Cwd          string
	Branch       string
	Model        string
	Originator   string
	Title        string
	AutoTitle    string
	AgentName    string
	FirstAt      string
	LastAt       string
	FileMtime    string
	MessageCount int64
	Name         string // given on the hub; wins over everything else
	Live         bool
	RunID        int64 // the fleet run this session belongs to, if any
}

// DisplayTitle prefers a person's own words, then a generated title.
func (s Session) DisplayTitle() string {
	switch {
	case s.Name != "":
		return s.Name
	case s.AutoTitle != "" && strings.HasPrefix(s.AgentName, "fleet-"):
		return s.AutoTitle
	case s.Title != "":
		return s.Title
	case s.AutoTitle != "":
		return s.AutoTitle
	case s.AgentName != "":
		return s.AgentName
	}
	return "(untitled)"
}

type Message struct {
	ID        int64
	SessionID int64
	Role      string
	Kind      string
	Meta      bool
	TS        string
	Text      string
	ToolName  string
	ToolUseID string
	ToolInput string
	IsError   bool
	Raw       string
}

type Event struct {
	Host, TS, Level, Text string
}

type Host struct {
	Host, Role, TZ, LastSeen string
	Running                  string
	Sessions, Runs, Messages int64
}

// LiveWindow is how recently a transcript must have been written to count as a live session.
const LiveWindow = 2 * time.Minute

// HostWindow is how recently a host must have checked in for its "running" workers to be believed.
const HostWindow = 2 * time.Minute

func ago(d time.Duration) string { return T(time.Now().Add(-d)) }

const runCols = `r.id, r.host, r.path, r.key, r.kind, r.target, r.provider, r.started_at, r.ended_at, r.status, r.summary,
	r.log_bytes, COALESCE(r.session_id, 0), COALESCE(h.last_seen, '')`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var seen string
	dst := []any{&r.ID, &r.Host, &r.Path, &r.Key, &r.Kind, &r.Target, &r.Provider, &r.StartedAt, &r.EndedAt,
		&r.Status, &r.Summary, &r.LogBytes, &r.SessionID, &seen}
	err := sc.Scan(dst...)
	r.HostLive = seen >= ago(HostWindow)
	return r, err
}

type RunFilter struct {
	Kind, Status, Host, Provider, Q string
	Limit                           int
}

func (s *Store) Runs(ctx context.Context, f RunFilter) ([]Run, error) {
	q := `SELECT ` + runCols + ` FROM runs r LEFT JOIN hosts h ON h.host = r.host WHERE 1=1`
	var args []any
	for col, v := range map[string]string{"r.kind": f.Kind, "r.status": f.Status, "r.host": f.Host, "r.provider": f.Provider} {
		if v != "" {
			q += ` AND ` + col + ` = ?`
			args = append(args, v)
		}
	}
	if f.Q != "" {
		q += ` AND (r.key LIKE ? ESCAPE '\' OR r.target LIKE ? ESCAPE '\' OR r.summary LIKE ? ESCAPE '\' OR r.log LIKE ? ESCAPE '\')`
		l := Like(f.Q)
		args = append(args, l, l, l, l)
	}
	if f.Limit <= 0 {
		f.Limit = 300
	}
	q += ` ORDER BY r.started_at DESC LIMIT ?`
	args = append(args, f.Limit)
	return s.queryRuns(ctx, q, args...)
}

func (s *Store) RunningRuns(ctx context.Context) ([]Run, error) {
	return s.queryRuns(ctx, `SELECT `+runCols+` FROM runs r LEFT JOIN hosts h ON h.host = r.host WHERE r.ended_at = ''
		ORDER BY r.started_at DESC`)
}

func (s *Store) queryRuns(ctx context.Context, q string, args ...any) ([]Run, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Run(ctx context.Context, id int64) (Run, error) {
	r, err := scanRun(s.DB.QueryRowContext(ctx, `SELECT `+runCols+` FROM runs r LEFT JOIN hosts h ON h.host = r.host
		WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = s.DB.QueryRowContext(ctx, `SELECT log, prompt FROM runs WHERE id = ?`, id).Scan(&r.Log, &r.Prompt)
	return r, err
}

// RunByLog finds a run by its log file name (the status.json snapshot names logs, not runs).
func (s *Store) RunByLog(ctx context.Context, host, name string) (int64, error) {
	var id int64
	q := `SELECT id FROM runs WHERE (path = ? OR path LIKE ? ESCAPE '\')`
	args := []any{name, "%/" + escapeLike(name)}
	if host != "" {
		q += ` AND host = ?`
		args = append(args, host)
	}
	err := s.DB.QueryRowContext(ctx, q+` ORDER BY started_at DESC LIMIT 1`, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) RunLog(ctx context.Context, id int64) (log, status, ended string, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT log, status, ended_at FROM runs WHERE id = ?`, id).Scan(&log, &status, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

const sessCols = `s.id, s.host, s.path, s.provider, s.session_uuid, s.parent_uuid, s.project, s.cwd, s.branch, s.model,
	s.originator, s.title, s.auto_title, s.agent_name, s.first_at, s.last_at, s.file_mtime, s.message_count, s.name`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var x Session
	err := sc.Scan(&x.ID, &x.Host, &x.Path, &x.Provider, &x.UUID, &x.ParentUUID, &x.Project, &x.Cwd, &x.Branch, &x.Model,
		&x.Originator, &x.Title, &x.AutoTitle, &x.AgentName, &x.FirstAt, &x.LastAt, &x.FileMtime, &x.MessageCount, &x.Name)
	x.Live = x.FileMtime >= ago(LiveWindow)
	return x, err
}

type SessionFilter struct {
	Host, Project, Provider, Model, Q string
	Fleet                             string // "" all, "only", "hide"
	Limit                             int
}

func (s *Store) Sessions(ctx context.Context, f SessionFilter) ([]Session, error) {
	q := `SELECT ` + sessCols + ` FROM sessions s WHERE s.parent_uuid = '' AND s.message_count > 0`
	var args []any
	if f.Host != "" {
		q += ` AND s.host = ?`
		args = append(args, f.Host)
	}
	if f.Provider != "" {
		q += ` AND s.provider = ?`
		args = append(args, f.Provider)
	}
	if f.Model != "" {
		q += ` AND s.model = ?`
		args = append(args, f.Model)
	}
	if f.Project != "" {
		q += ` AND (s.project = ? OR s.cwd = ?)`
		args = append(args, f.Project, f.Project)
	}
	switch f.Fleet {
	case "only":
		q += ` AND s.agent_name LIKE 'fleet-%'`
	case "hide":
		q += ` AND s.agent_name NOT LIKE 'fleet-%'`
	}
	if f.Q != "" {
		l := Like(f.Q)
		q += ` AND (s.title LIKE ? ESCAPE '\' OR s.auto_title LIKE ? ESCAPE '\' OR s.cwd LIKE ? ESCAPE '\' OR s.branch LIKE ? ESCAPE '\'
			OR s.agent_name LIKE ? ESCAPE '\' OR s.session_uuid = ?)`
		args = append(args, l, l, l, l, l, f.Q)
	}
	if f.Limit <= 0 {
		f.Limit = 300
	}
	q += ` ORDER BY s.file_mtime DESC LIMIT ?`
	args = append(args, f.Limit)
	return s.querySessions(ctx, q, args...)
}

func (s *Store) LiveSessions(ctx context.Context) ([]Session, error) {
	return s.querySessions(ctx, `SELECT `+sessCols+` FROM sessions s WHERE s.parent_uuid = '' AND s.file_mtime >= ?
		ORDER BY s.file_mtime DESC`, ago(LiveWindow))
}

func (s *Store) Subagents(ctx context.Context, host, parentUUID string) ([]Session, error) {
	return s.querySessions(ctx, `SELECT `+sessCols+` FROM sessions s WHERE s.host = ? AND s.parent_uuid = ? ORDER BY s.first_at`,
		host, parentUUID)
}

func (s *Store) querySessions(ctx context.Context, q string, args ...any) ([]Session, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) Session(ctx context.Context, id int64) (Session, error) {
	x, err := scanSession(s.DB.QueryRowContext(ctx, `SELECT `+sessCols+` FROM sessions s WHERE s.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err == nil {
		_ = s.DB.QueryRowContext(ctx, `SELECT id FROM runs WHERE session_id = ? LIMIT 1`, id).Scan(&x.RunID)
	}
	return x, err
}

// SessionByUUID resolves a parent session uuid (subagent transcripts point at their parent by uuid).
func (s *Store) SessionByUUID(ctx context.Context, host, uuid string) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM sessions WHERE host = ? AND session_uuid = ? AND parent_uuid = '' LIMIT 1`,
		host, uuid).Scan(&id)
	return id, err
}

const msgCols = `id, session_id, role, kind, meta, ts, text, tool_name, tool_use_id, tool_input, is_error`

func (s *Store) queryMessages(ctx context.Context, q string, args ...any) ([]Message, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var meta, iserr int
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Kind, &meta, &m.TS, &m.Text, &m.ToolName, &m.ToolUseID, &m.ToolInput, &iserr); err != nil {
			return nil, err
		}
		m.Meta, m.IsError = meta != 0, iserr != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// Messages pages a transcript in file order. Messages are inserted in file order per session, so the id
// order is the transcript order. after>0: the page after that id; before>0: the page ending before it;
// neither: the last page.
func (s *Store) Messages(ctx context.Context, sessionID, after, before int64, limit int) ([]Message, error) {
	switch {
	case after > 0:
		return s.queryMessages(ctx, `SELECT `+msgCols+` FROM messages WHERE session_id = ? AND id > ? ORDER BY id LIMIT ?`,
			sessionID, after, limit)
	case before > 0:
		ms, err := s.queryMessages(ctx, `SELECT `+msgCols+` FROM messages WHERE session_id = ? AND id < ? ORDER BY id DESC LIMIT ?`,
			sessionID, before, limit)
		reverse(ms)
		return ms, err
	default:
		ms, err := s.queryMessages(ctx, `SELECT `+msgCols+` FROM messages WHERE session_id = ? ORDER BY id DESC LIMIT ?`,
			sessionID, limit)
		reverse(ms)
		return ms, err
	}
}

func (s *Store) MessageRaw(ctx context.Context, id int64) (string, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT raw FROM messages WHERE id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return raw, err
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

type Hit struct {
	Message
	Session Session
}

// Search is a LIKE scan over message text, tool input and tool output (Turso has no FTS5 here).
func (s *Store) Search(ctx context.Context, q, host string, limit int) ([]Hit, error) {
	l := Like(q)
	sqlq := `SELECT m.id, m.session_id, m.role, m.kind, m.meta, m.ts, m.text, m.tool_name, m.tool_use_id, m.tool_input, m.is_error,
		` + sessCols + ` FROM messages m JOIN sessions s ON s.id = m.session_id
		WHERE (m.text LIKE ? ESCAPE '\' OR m.tool_input LIKE ? ESCAPE '\')`
	args := []any{l, l}
	if host != "" {
		sqlq += ` AND s.host = ?`
		args = append(args, host)
	}
	sqlq += ` ORDER BY m.id DESC LIMIT ?`
	args = append(args, limit)
	return s.queryHits(ctx, sqlq, args...)
}

func (s *Store) queryHits(ctx context.Context, sqlq string, args ...any) ([]Hit, error) {
	rows, err := s.DB.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var meta, iserr int
		x := &h.Session
		if err := rows.Scan(&h.ID, &h.SessionID, &h.Role, &h.Kind, &meta, &h.TS, &h.Text, &h.ToolName, &h.ToolUseID, &h.ToolInput, &iserr,
			&x.ID, &x.Host, &x.Path, &x.Provider, &x.UUID, &x.ParentUUID, &x.Project, &x.Cwd, &x.Branch, &x.Model,
			&x.Originator, &x.Title, &x.AutoTitle, &x.AgentName, &x.FirstAt, &x.LastAt, &x.FileMtime, &x.MessageCount, &x.Name); err != nil {
			return nil, err
		}
		h.Meta, h.IsError = meta != 0, iserr != 0
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) Events(ctx context.Context, limit int) ([]Event, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT host, ts, level, text FROM events ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Host, &e.TS, &e.Level, &e.Text); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	reverse(out)
	return out, rows.Err()
}

// Hosts lists every host that has data or has checked in, with row counts.
func (s *Store) Hosts(ctx context.Context) ([]Host, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT x.host, COALESCE(h.role, ''), COALESCE(h.tz, ''), COALESCE(h.last_seen, ''), COALESCE(h.running, '[]'),
		(SELECT count(*) FROM sessions s WHERE s.host = x.host),
		(SELECT count(*) FROM runs r WHERE r.host = x.host),
		(SELECT count(*) FROM messages m JOIN sessions s ON s.id = m.session_id WHERE s.host = x.host)
		FROM (SELECT host FROM files UNION SELECT host FROM hosts) x LEFT JOIN hosts h ON h.host = x.host ORDER BY x.host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Host
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.Host, &h.Role, &h.TZ, &h.LastSeen, &h.Running, &h.Sessions, &h.Runs, &h.Messages); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Distinct values for filter menus.
func (s *Store) Distinct(ctx context.Context, table, col string) ([]string, error) {
	switch table + "." + col {
	case "runs.host", "runs.provider", "runs.kind", "runs.status", "sessions.host", "sessions.provider":
	default:
		return nil, errors.New("not allowed")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT `+col+` FROM `+table+` WHERE `+col+` != '' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Like wraps a user query as a LIKE pattern with % and _ escaped.
func Like(q string) string { return "%" + escapeLike(q) + "%" }

func escapeLike(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(q)
}

// ModelUsage describes recorded activity, not a provider's subscription allowance.
type ModelUsage struct {
	Provider, Model          string
	Sessions, Messages, Live int64
	LastAt                   string
}

func (s *Store) ModelUsage(ctx context.Context) ([]ModelUsage, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT provider, model, count(*), sum(message_count),
 sum(CASE WHEN parent_uuid = '' AND file_mtime >= ? THEN 1 ELSE 0 END), max(file_mtime)
 FROM sessions WHERE message_count > 0 GROUP BY provider, model ORDER BY provider, model`, ago(LiveWindow))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelUsage
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Provider, &m.Model, &m.Sessions, &m.Messages, &m.Live, &m.LastAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type QuotaObservation struct {
	Host, Provider, ObservedAt string
	parse.Quota
}

func (q QuotaObservation) Stale() bool { return time.Since(ParseT(q.ObservedAt)) > 15*time.Minute }
func (s *Store) Quotas(ctx context.Context) ([]QuotaObservation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT host, provider, observed_at, payload FROM quota_observations ORDER BY provider, host, limit_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuotaObservation
	for rows.Next() {
		var q QuotaObservation
		var payload string
		if err := rows.Scan(&q.Host, &q.Provider, &q.ObservedAt, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &q.Quota); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
