// Package insights computes the hub's analytics: a background rollup that condenses the messages table into
// small derived tables (usage_events, tool_calls, activity), the aggregate queries behind /insights and the
// overview, a price table for API-equivalent cost estimates, and rules that turn the numbers into plain-English
// findings. Nothing here calls an LLM.
package insights

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Rollup walks messages in id order and keeps the derived tables current. Messages are append-only and ids
// only grow, so a cursor (the last id processed) is all the state it needs.
type Rollup struct {
	Store *store.Store
	Log   *log.Logger
	// Batch is how many message ids one step covers (default 1000). Steps are short transactions so page
	// renders, which share the store's single connection, never wait long behind one.
	Batch int
	// OnCatchUp is called when a run of batches reaches the newest message (e.g. to drop cached reports).
	OnCatchUp func()
}

const cursorName = "messages"

// lastMessageID reads the newest message id. (Turso v0.7 answers MAX(id) with a full table scan; an ordered
// LIMIT 1 walks the rowid b-tree from the end.)
const lastMessageID = `SELECT COALESCE((SELECT id FROM messages ORDER BY id DESC LIMIT 1), 0)`

// Run steps until caught up, then polls every `every`.
func (r *Rollup) Run(ctx context.Context, every time.Duration) {
	busy := false
	for {
		n, err := r.Step(ctx)
		if err != nil && ctx.Err() == nil && r.Log != nil {
			r.Log.Printf("insights rollup: %v", err)
		}
		wait := every
		switch {
		case n > 0 && err == nil:
			wait = 15 * time.Millisecond // backfilling: yield the connection briefly between batches
			busy = true
		case busy && err == nil:
			busy = false
			if r.OnCatchUp != nil {
				r.OnCatchUp()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// CatchUp runs steps until no messages are left (tests, one-shot tools).
func (r *Rollup) CatchUp(ctx context.Context) error {
	for {
		n, err := r.Step(ctx)
		if err != nil || n == 0 {
			return err
		}
	}
}

type usageLine struct {
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input      int64 `json:"input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
			Output     int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type usageRow struct {
	session                           int64
	apiID, ts, model                  string
	input, output, cacheRead, cacheWr int64
}

type toolRow struct {
	id, session int64
	ts, tool    string
	useID       string
}

type actKey struct {
	hour    string
	session int64
}

// Step processes the next Batch message ids and returns how many rows it read (0: caught up).
func (r *Rollup) Step(ctx context.Context) (int, error) {
	db := r.Store.DB
	batch := r.Batch
	if batch <= 0 {
		batch = 1000
	}
	var last, top int64
	if err := db.QueryRowContext(ctx, `SELECT last_id FROM insights_cursor WHERE name = ?`, cursorName).Scan(&last); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err := db.QueryRowContext(ctx, lastMessageID).Scan(&top); err != nil {
		return 0, err
	}
	if top <= last {
		return 0, nil
	}
	hi := min(top, last+int64(batch))
	rows, err := db.QueryContext(ctx, `SELECT id, session_id, role, kind, meta, ts, tool_name, tool_use_id, is_error,
		CASE WHEN role = 'assistant' AND raw LIKE '%"usage"%' THEN raw ELSE '' END
		FROM messages WHERE id > ? AND id <= ? ORDER BY id`, last, hi)
	if err != nil {
		return 0, err
	}
	act := map[actKey][2]int64{}
	var usage []usageRow
	var tools []toolRow
	type errRef struct {
		session int64
		useID   string
	}
	var errs []errRef
	missingTS := map[int64]bool{}
	type pending struct {
		session           int64
		role, kind, raw   string
		meta              bool
		toolName, toolUse string
		isErr             bool
		id                int64
	}
	var noTS []pending
	n := 0
	handle := func(id, sid int64, role, kind string, meta bool, ts, toolName, toolUse string, isErr bool, raw string) {
		hour := ""
		if len(ts) >= 13 {
			hour = ts[:13]
		}
		k := actKey{hour, sid}
		c := act[k]
		c[0]++
		if role == "user" && kind == "text" && !meta {
			c[1]++
		}
		act[k] = c
		switch {
		case kind == "tool_use" && toolName != "":
			tools = append(tools, toolRow{id: id, session: sid, ts: ts, tool: toolName, useID: toolUse})
		case kind == "tool_result" && isErr && toolUse != "":
			errs = append(errs, errRef{sid, toolUse})
		}
		if raw != "" {
			var u usageLine
			if json.Unmarshal([]byte(raw), &u) == nil && u.Message.Usage != nil && u.Message.ID != "" &&
				!strings.HasPrefix(u.Message.Model, "<") {
				x := u.Message.Usage
				usage = append(usage, usageRow{session: sid, apiID: u.Message.ID, ts: ts, model: u.Message.Model,
					input: x.Input, output: x.Output, cacheRead: x.CacheRead, cacheWr: x.CacheWrite})
			}
		}
	}
	for rows.Next() {
		var id, sid int64
		var role, kind, ts, toolName, toolUse, raw string
		var meta, isErr int
		if err := rows.Scan(&id, &sid, &role, &kind, &meta, &ts, &toolName, &toolUse, &isErr, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		n++
		if ts == "" {
			missingTS[sid] = true
			noTS = append(noTS, pending{sid, role, kind, raw, meta != 0, toolName, toolUse, isErr != 0, id})
			continue
		}
		handle(id, sid, role, kind, meta != 0, ts, toolName, toolUse, isErr != 0, raw)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Old Codex rollouts have no per-line timestamps: date those blocks at their session's start.
	if len(noTS) > 0 {
		first := map[int64]string{}
		for sid := range missingTS {
			var f string
			_ = db.QueryRowContext(ctx, `SELECT first_at FROM sessions WHERE id = ?`, sid).Scan(&f)
			first[sid] = f
		}
		for _, p := range noTS {
			if ts := first[p.session]; ts != "" {
				handle(p.id, p.session, p.role, p.kind, p.meta, ts, p.toolName, p.toolUse, p.isErr, p.raw)
			}
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for k, c := range act {
		if k.hour == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO activity (hour, session_id, blocks, prompts) VALUES (?, ?, ?, ?)
			ON CONFLICT (hour, session_id) DO UPDATE SET blocks = activity.blocks + excluded.blocks,
			prompts = activity.prompts + excluded.prompts`, k.hour, k.session, c[0], c[1]); err != nil {
			return 0, err
		}
	}
	// A response is written as several lines that repeat its usage; later lines can carry the final output
	// count, so keep the largest of each figure.
	for _, u := range usage {
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_events (session_id, api_id, ts, model, input, output, cache_read, cache_write)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (session_id, api_id) DO UPDATE SET
			input = max(usage_events.input, excluded.input), output = max(usage_events.output, excluded.output),
			cache_read = max(usage_events.cache_read, excluded.cache_read), cache_write = max(usage_events.cache_write, excluded.cache_write)`,
			u.session, u.apiID, u.ts, u.model, u.input, u.output, u.cacheRead, u.cacheWr); err != nil {
			return 0, err
		}
	}
	for _, t := range tools {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tool_calls (message_id, session_id, ts, tool, use_id) VALUES (?, ?, ?, ?, ?)`,
			t.id, t.session, t.ts, t.tool, t.useID); err != nil {
			return 0, err
		}
	}
	for _, e := range errs {
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET is_error = 1 WHERE session_id = ? AND use_id = ?`, e.session, e.useID); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO insights_cursor (name, last_id) VALUES (?, ?)
		ON CONFLICT (name) DO UPDATE SET last_id = excluded.last_id`, cursorName, hi); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return max(n, 1), nil // an empty id window still advanced the cursor
}

// Progress reports how far the rollup has read, as a percentage of the newest message id.
func Progress(ctx context.Context, st *store.Store) int {
	var last, top int64
	_ = st.DB.QueryRowContext(ctx, `SELECT last_id FROM insights_cursor WHERE name = ?`, cursorName).Scan(&last)
	_ = st.DB.QueryRowContext(ctx, lastMessageID).Scan(&top)
	if top == 0 || last >= top {
		return 100
	}
	return int(last * 100 / top)
}
