// Package ingest moves source files into the store. The unit of work is a Chunk: some bytes of one file on one
// host, starting at a byte offset. A Collector reads chunks off the local disk and hands them to a Sink; on the
// hub the Sink is the Engine (straight into the database), on other machines it is the HTTP shipper. Either
// way the Engine is the only code that turns bytes into rows, and the file's offset advances in the same
// transaction as the rows it produced.
package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // fleet machines report IANA zone names; don't depend on the host's zoneinfo
	"unicode/utf8"

	"github.com/anchoo2kewl/llm-brain/hub/internal/parse"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// File kinds.
const (
	KindClaude   = "claude"
	KindCodex    = "codex"
	KindRunLog   = "runlog"
	KindFleetLog = "fleetlog"
)

type Chunk struct {
	Host   string
	Kind   string
	Path   string
	Offset int64 // where Data starts in the file
	Size   int64 // file size when read
	Mtime  time.Time
	TZ     string // IANA zone of the machine that wrote the file
	Reset  bool   // the file shrank: forget what was read from it and start over
	EOF    bool   // Data reaches the end of the file
	Data   []byte
}

type Prompt struct {
	Key   string    `json:"key"`
	Mtime time.Time `json:"mtime"`
	Text  string    `json:"text"`
}

type Heartbeat struct {
	Host    string   `json:"host"`
	Role    string   `json:"role"`
	TZ      string   `json:"tz"`
	Running []string `json:"running"` // fleet worker keys whose pid is alive
	Prompts []Prompt `json:"prompts"`
}

// Sink receives chunks. Chunk returns the file's new offset; on ErrOffset the returned offset is where the
// sink actually is, and the caller resumes from there.
type Sink interface {
	Offsets(ctx context.Context, host string) (map[string]int64, error)
	Chunk(ctx context.Context, c Chunk) (int64, error)
	Heartbeat(ctx context.Context, h Heartbeat) error
}

var ErrOffset = errors.New("offset mismatch")

// Engine is the Sink that writes to the store.
type Engine struct {
	S         *store.Store
	Org       string // GitHub org for pr-/review- keys
	IssueRepo string // owner/repo for issue- keys
	Now       func() time.Time
	// OnWrite, when set, runs after every committed write (the search index catches up here, in the same
	// pass that ingested the rows).
	OnWrite func(context.Context)

	mu     sync.Mutex
	linked map[string]bool // hosts that had their full link pass this process
}

func NewEngine(s *store.Store, org, issueRepo string) *Engine {
	return &Engine{S: s, Org: org, IssueRepo: issueRepo, Now: time.Now, linked: map[string]bool{}}
}

func (e *Engine) Offsets(ctx context.Context, host string) (map[string]int64, error) {
	rows, err := e.S.DB.QueryContext(ctx, `SELECT path, off FROM files WHERE host = ?`, host)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var p string
		var o int64
		if err := rows.Scan(&p, &o); err != nil {
			return nil, err
		}
		out[p] = o
	}
	return out, rows.Err()
}

var locCache sync.Map

func Location(name string) *time.Location {
	if name == "" || name == "Local" {
		return time.Local
	}
	if l, ok := locCache.Load(name); ok {
		return l.(*time.Location)
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		l = time.Local
	}
	locCache.Store(name, l)
	return l
}

// consumable is how many bytes of a chunk can be applied now: up to the last newline, since the writer may be
// mid-line. A worker log that ends without a newline and has been quiet for 30 s is taken whole (up to a whole
// rune), so its last line still shows up.
func consumable(c Chunk) int {
	n := bytes.LastIndexByte(c.Data, '\n') + 1
	if c.Kind != KindRunLog || n == len(c.Data) || !c.EOF || time.Since(c.Mtime) < 30*time.Second {
		return n
	}
	end := len(c.Data)
	for i := end - 1; i >= 0 && i >= end-utf8.UTFMax; i-- {
		if utf8.RuneStart(c.Data[i]) {
			if utf8.FullRune(c.Data[i:]) {
				return end
			}
			return i
		}
	}
	return end
}

func (e *Engine) Chunk(ctx context.Context, c Chunk) (int64, error) {
	if c.Host == "" || c.Path == "" {
		return 0, errors.New("chunk needs host and path")
	}
	switch c.Kind {
	case KindClaude, KindCodex, KindRunLog, KindFleetLog:
	default:
		return 0, fmt.Errorf("unknown kind %q", c.Kind)
	}
	tx, err := e.S.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var off int64
	switch err := tx.QueryRowContext(ctx, `SELECT off FROM files WHERE host = ? AND path = ?`, c.Host, c.Path).Scan(&off); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return 0, err
	}
	if c.Reset && off > 0 {
		if err := e.reset(ctx, tx, c); err != nil {
			return 0, err
		}
		off = 0
	}
	if c.Offset != off {
		return off, ErrOffset
	}
	n := consumable(c)
	// A worker log is registered as a run as soon as it exists: `claude -p` writes nothing until it finishes.
	if n > 0 || c.Kind == KindRunLog {
		data := c.Data[:n]
		switch c.Kind {
		case KindClaude, KindCodex:
			err = e.applySession(ctx, tx, c, data)
		case KindRunLog:
			err = e.applyRunLog(ctx, tx, c, data)
		case KindFleetLog:
			err = e.applyFleetLog(ctx, tx, c, data)
		}
		if err != nil {
			return 0, fmt.Errorf("%s %s@%d: %w", c.Kind, c.Path, c.Offset, err)
		}
	}
	newOff := off + int64(n)
	if _, err := tx.ExecContext(ctx, `INSERT INTO files (host, path, kind, off, size, mtime, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (host, path) DO UPDATE SET off = excluded.off, size = excluded.size, mtime = excluded.mtime,
		updated_at = excluded.updated_at, kind = excluded.kind`,
		c.Host, c.Path, c.Kind, newOff, c.Size, store.T(c.Mtime), store.Now()); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	e.wrote(ctx)
	return newOff, nil
}

func (e *Engine) wrote(ctx context.Context) {
	if e.OnWrite != nil {
		e.OnWrite(ctx)
	}
}

func (e *Engine) reset(ctx context.Context, tx *sql.Tx, c Chunk) error {
	var err error
	switch c.Kind {
	case KindClaude, KindCodex:
		if _, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE session_id IN (SELECT id FROM sessions WHERE host = ? AND path = ?)`, c.Host, c.Path); err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE sessions SET message_count = 0 WHERE host = ? AND path = ?`, c.Host, c.Path)
		}
		if err == nil {
			err = store.ResetSessionInsights(ctx, tx, c.Host, c.Path)
		}
	case KindRunLog:
		_, err = tx.ExecContext(ctx, `UPDATE runs SET log = '', log_bytes = 0 WHERE host = ? AND path = ?`, c.Host, c.Path)
	case KindFleetLog:
		_, err = tx.ExecContext(ctx, `DELETE FROM events WHERE host = ? AND path = ?`, c.Host, c.Path)
	}
	return err
}

// ---------------------------------------------------------------- sessions

type sessionMeta struct {
	id                                                    int64
	uuid, parent, project, cwd, branch, model, originator string
	title, autoTitle, agentName, firstAt, lastAt          string
	count                                                 int64
}

// sessionPathInfo derives the session uuid, parent and project from a transcript path.
//
//	claude: <projects>/<project>/<uuid>.jsonl, subagents at <projects>/<project>/<parent-uuid>/subagents/agent-x.jsonl
//	codex:  <sessions>/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl
func sessionPathInfo(kind, path string) (uuid, parent, project string) {
	dir := filepath.Dir(path)
	if kind == KindCodex {
		return parse.CodexSessionID(path), "", ""
	}
	uuid = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if filepath.Base(dir) == "subagents" {
		pdir := filepath.Dir(dir)
		return uuid, filepath.Base(pdir), filepath.Base(filepath.Dir(pdir))
	}
	return uuid, "", filepath.Base(dir)
}

func (e *Engine) applySession(ctx context.Context, tx *sql.Tx, c Chunk, data []byte) error {
	uuid, parent, project := sessionPathInfo(c.Kind, c.Path)
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (host, path, provider, session_uuid, parent_uuid, project)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (host, path) DO NOTHING`, c.Host, c.Path, c.Kind, uuid, parent, project); err != nil {
		return err
	}
	var m sessionMeta
	if err := tx.QueryRowContext(ctx, `SELECT id, session_uuid, parent_uuid, project, cwd, branch, model, originator, title,
		auto_title, agent_name, first_at, last_at, message_count FROM sessions WHERE host = ? AND path = ?`, c.Host, c.Path).
		Scan(&m.id, &m.uuid, &m.parent, &m.project, &m.cwd, &m.branch, &m.model, &m.originator, &m.title,
			&m.autoTitle, &m.agentName, &m.firstAt, &m.lastAt, &m.count); err != nil {
		return err
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO messages (session_id, line_off, idx, role, kind, meta, ts, text, tool_name,
		tool_use_id, tool_input, is_error, raw) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	defer ins.Close()

	pos := 0
	for pos < len(data) {
		end := bytes.IndexByte(data[pos:], '\n')
		if end < 0 {
			end = len(data) - pos
		}
		line := bytes.TrimSpace(data[pos : pos+end])
		lineOff := c.Offset + int64(pos)
		pos += end + 1
		if len(line) == 0 {
			continue
		}
		var ev parse.Event
		var perr error
		if c.Kind == KindCodex {
			ev, perr = parse.Codex(line)
		} else {
			ev, perr = parse.Claude(line)
		}
		if perr != nil {
			continue // a corrupt line is skipped, not fatal
		}
		ts := parse.FormatTime(ev.Timestamp)
		if ts != "" {
			if m.firstAt == "" || ts < m.firstAt {
				m.firstAt = ts
			}
			if ts > m.lastAt {
				m.lastAt = ts
			}
		} else {
			ts = m.lastAt // 2025 Codex items carry no timestamp
		}
		if ev.Quota != nil && ts != "" {
			payload, err := json.Marshal(ev.Quota)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO quota_observations (host, provider, limit_id, observed_at, payload)
                VALUES (?, 'codex', ?, ?, ?) ON CONFLICT (host, provider, limit_id) DO UPDATE
                SET observed_at = excluded.observed_at, payload = excluded.payload
                WHERE excluded.observed_at > quota_observations.observed_at`, c.Host, ev.Quota.LimitID, ts, string(payload)); err != nil {
				return err
			}
		}
		setIf(&m.cwd, ev.Cwd)
		setIf(&m.branch, ev.Branch)
		setIf(&m.model, ev.Model)
		setIf(&m.autoTitle, ev.AutoTitle)
		setIf(&m.agentName, ev.AgentName)
		if m.originator == "" {
			m.originator = ev.Originator
		}
		if m.title == "" && ev.FirstPrompt != "" {
			m.title = parse.TitleFrom(ev.FirstPrompt)
		}
		// Usage is taken from the parsed line, not from raw (which is capped and may not parse back).
		if u := ev.Usage; u != nil && ts != "" {
			if err := store.UpsertUsage(ctx, tx, store.Usage{SessionID: m.id, APIID: u.ID, TS: ts, Model: u.Model,
				Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}); err != nil {
				return err
			}
		}
		for i, b := range ev.Blocks {
			raw := ""
			if i == 0 {
				raw = ev.Raw
			}
			res, err := ins.ExecContext(ctx, m.id, lineOff, i, b.Role, b.Kind, b2i(b.Meta), ts, b.Text, b.ToolName,
				b.ToolUseID, b.ToolInput, b2i(b.IsError), raw)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				m.count += n
			}
		}
	}
	if c.Kind == KindCodex && m.project == "" {
		m.project = m.cwd
	}
	_, err = tx.ExecContext(ctx, `UPDATE sessions SET project = ?, cwd = ?, branch = ?, model = ?, originator = ?, title = ?,
		auto_title = ?, agent_name = ?, first_at = ?, last_at = ?, file_mtime = ?, message_count = ? WHERE id = ?`,
		m.project, m.cwd, m.branch, m.model, m.originator, m.title, m.autoTitle, m.agentName, m.firstAt, m.lastAt,
		store.T(c.Mtime), m.count, m.id)
	if err != nil {
		return err
	}
	return store.MarkDirty(ctx, tx, "s", m.id)
}

func setIf(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------- worker logs

func (e *Engine) applyRunLog(ctx context.Context, tx *sql.Tx, c Chunk, data []byte) error {
	rl, ok := parse.ParseRunLogName(filepath.Base(c.Path), Location(c.TZ), e.Org, e.IssueRepo)
	if !ok {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs (host, path, key, kind, target, started_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (host, path) DO NOTHING`, c.Host, c.Path, rl.Key, rl.Kind, rl.Target, store.T(rl.Started)); err != nil {
		return err
	}
	text := string(data)
	var id int64
	var summary, provider, target string
	if err := tx.QueryRowContext(ctx, `SELECT id, summary, provider, target FROM runs WHERE host = ? AND path = ?`, c.Host, c.Path).
		Scan(&id, &summary, &provider, &target); err != nil {
		return err
	}
	if r, ok := parse.FindReviewed(text); ok {
		summary = strings.TrimSpace("REVIEWED " + r.URL + " " + r.Verdict + " " + r.Detail)
		if strings.HasPrefix(r.URL, "https://") {
			target = r.URL
		}
	} else if !strings.HasPrefix(summary, "REVIEWED ") {
		if ll := parse.LastLine(text); ll != "" {
			summary = ll
		}
	}
	if provider == "" && c.Offset == 0 && text != "" {
		provider = "claude"
		if parse.LooksLikeCodex(text[:min(len(text), 4096)]) {
			provider = "codex"
		}
	}
	// A review that was closed as failed before its REVIEWED line arrived (shipped late) is a success after all.
	_, err := tx.ExecContext(ctx, `UPDATE runs SET log = log || ?, log_bytes = log_bytes + ?, summary = ?, provider = ?, target = ?,
		status = CASE WHEN status = 'failed' AND ? THEN 'succeeded' ELSE status END
		WHERE host = ? AND path = ?`, text, len(data), summary, provider, target, strings.HasPrefix(summary, "REVIEWED "), c.Host, c.Path)
	if err != nil {
		return err
	}
	return store.MarkDirty(ctx, tx, "r", id)
}

// ---------------------------------------------------------------- scheduler log

func (e *Engine) applyFleetLog(ctx context.Context, tx *sql.Tx, c Chunk, data []byte) error {
	ins, err := tx.PrepareContext(ctx, `INSERT INTO events (host, path, line_off, ts, level, text) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	defer ins.Close()
	loc := Location(c.TZ)
	last := store.T(c.Mtime)
	pos := 0
	for pos < len(data) {
		end := bytes.IndexByte(data[pos:], '\n')
		if end < 0 {
			end = len(data) - pos
		}
		line := strings.TrimRight(string(data[pos:pos+end]), "\r")
		lineOff := c.Offset + int64(pos)
		pos += end + 1
		if strings.TrimSpace(line) == "" {
			continue
		}
		fl := parse.ParseFleetLine(line, loc)
		ts := last
		if !fl.At.IsZero() {
			ts = store.T(fl.At)
			last = ts
		}
		if _, err := ins.ExecContext(ctx, c.Host, c.Path, lineOff, ts, fl.Level, parse.Truncate(fl.Text, 8<<10)); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- heartbeat

// Heartbeat records which workers a host has alive, closes the runs that are no longer alive, attaches
// prompts, and links runs to their transcripts.
func (e *Engine) Heartbeat(ctx context.Context, h Heartbeat) error {
	if h.Host == "" {
		return errors.New("heartbeat needs host")
	}
	running, _ := json.Marshal(h.Running)
	if h.Running == nil {
		running = []byte("[]")
	}
	if _, err := e.S.DB.ExecContext(ctx, `INSERT INTO hosts (host, role, tz, running, last_seen) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (host) DO UPDATE SET role = excluded.role, tz = excluded.tz, running = excluded.running,
		last_seen = excluded.last_seen`, h.Host, h.Role, h.TZ, string(running), store.T(e.Now())); err != nil {
		return err
	}
	if err := e.finalize(ctx, h); err != nil {
		return err
	}
	if err := e.attachPrompts(ctx, h); err != nil {
		return err
	}
	if err := e.link(ctx, h.Host); err != nil {
		return err
	}
	e.wrote(ctx)
	return nil
}

func (e *Engine) finalize(ctx context.Context, h Heartbeat) error {
	alive := map[string]bool{}
	for _, k := range h.Running {
		alive[k] = true
	}
	type open struct {
		id                                    int64
		key, path, started, kind, summary, pv string
	}
	rows, err := e.S.DB.QueryContext(ctx, `SELECT id, key, path, started_at, kind, summary, provider FROM runs WHERE host = ? AND ended_at = ''`, h.Host)
	if err != nil {
		return err
	}
	var opens []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.key, &o.path, &o.started, &o.kind, &o.summary, &o.pv); err != nil {
			rows.Close()
			return err
		}
		opens = append(opens, o)
	}
	rows.Close()
	for _, o := range opens {
		var latest string
		if err := e.S.DB.QueryRowContext(ctx, `SELECT max(started_at) FROM runs WHERE host = ? AND key = ?`, h.Host, o.key).Scan(&latest); err != nil {
			return err
		}
		if alive[o.key] && o.started == latest {
			continue
		}
		var ended string
		_ = e.S.DB.QueryRowContext(ctx, `SELECT mtime FROM files WHERE host = ? AND path = ?`, h.Host, o.path).Scan(&ended)
		if ended == "" || ended < o.started {
			ended = store.T(e.Now())
		}
		status := "exited"
		if o.kind == "review" {
			status = "failed"
			if strings.HasPrefix(o.summary, "REVIEWED ") {
				status = "succeeded"
			}
		}
		if o.pv == "" {
			o.pv = "claude"
		}
		if _, err := e.S.DB.ExecContext(ctx, `UPDATE runs SET ended_at = ?, status = ?, provider = ? WHERE id = ?`, ended, status, o.pv, o.id); err != nil {
			return err
		}
		if err := store.MarkDirty(ctx, e.S.DB, "r", o.id); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) attachPrompts(ctx context.Context, h Heartbeat) error {
	for _, p := range h.Prompts {
		var id int64
		var started, prompt string
		err := e.S.DB.QueryRowContext(ctx, `SELECT id, started_at, prompt FROM runs WHERE host = ? AND key = ? ORDER BY started_at DESC LIMIT 1`,
			h.Host, p.Key).Scan(&id, &started, &prompt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if prompt != "" || store.T(p.Mtime.Add(2*time.Minute)) < started {
			continue
		}
		if _, err := e.S.DB.ExecContext(ctx, `UPDATE runs SET prompt = ? WHERE id = ?`, p.Text, id); err != nil {
			return err
		}
		if err := store.MarkDirty(ctx, e.S.DB, "r", id); err != nil {
			return err
		}
	}
	return nil
}

// link finds each worker's own transcript: a Claude session named fleet-<key> (llm-run passes --name), or for
// Codex an exec session on the same host that started within minutes of the run. Best effort.
func (e *Engine) link(ctx context.Context, host string) error {
	e.mu.Lock()
	full := !e.linked[host]
	e.linked[host] = true
	e.mu.Unlock()
	q := `SELECT id, key, started_at, ended_at, provider FROM runs WHERE host = ? AND session_id IS NULL`
	args := []any{host}
	if !full {
		q += ` AND (ended_at = '' OR ended_at > ?)`
		args = append(args, store.T(e.Now().Add(-2*time.Hour)))
	}
	type cand struct {
		id                        int64
		key, started, ended, prov string
	}
	rows, err := e.S.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	var cs []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.key, &c.started, &c.ended, &c.prov); err != nil {
			rows.Close()
			return err
		}
		cs = append(cs, c)
	}
	rows.Close()
	for _, c := range cs {
		st := store.ParseT(c.started)
		from := store.T(st.Add(-2 * time.Minute))
		to := c.ended
		if to == "" {
			to = "9999"
		}
		var sid int64
		var prov string
		err := e.S.DB.QueryRowContext(ctx, `SELECT id, provider FROM sessions WHERE host = ? AND agent_name = ? AND first_at >= ? AND first_at <= ?
			ORDER BY first_at LIMIT 1`, host, "fleet-"+c.key, from, to).Scan(&sid, &prov)
		if errors.Is(err, sql.ErrNoRows) && c.prov == "codex" {
			err = e.S.DB.QueryRowContext(ctx, `SELECT id, provider FROM sessions WHERE host = ? AND provider = 'codex'
				AND originator LIKE 'codex_exec%' AND first_at >= ? AND first_at <= ?
				AND id NOT IN (SELECT session_id FROM runs WHERE session_id IS NOT NULL) ORDER BY first_at LIMIT 1`,
				host, from, store.T(st.Add(5*time.Minute))).Scan(&sid, &prov)
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := e.S.DB.ExecContext(ctx, `UPDATE runs SET session_id = ?, provider = ? WHERE id = ?`, sid, prov, c.id); err != nil {
			return err
		}
		if err := store.MarkDirty(ctx, e.S.DB, "r", c.id); err != nil {
			return err
		}
	}
	return nil
}
