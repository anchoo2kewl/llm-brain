package insights

import (
	"context"
	"sort"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// SessionStats sums one conversation and its subagents: tokens, estimated cost, tool calls, and token use
// over the conversation's lifetime (for a sparkline).
type SessionStats struct {
	Tokens     Tokens     `json:"tokens"`
	Dollars    float64    `json:"dollars"`
	Models     []string   `json:"models"`
	ToolCalls  int        `json:"tool_calls"`
	ToolErrors int        `json:"tool_errors"`
	Prompts    int64      `json:"prompts"`
	Responses  int        `json:"responses"`
	Subagents  int        `json:"subagents"`
	Spark      []float64  `json:"spark"` // tokens per slice of the session's span
	First      time.Time  `json:"first"`
	Last       time.Time  `json:"last"`
	TopTools   []ToolStat `json:"top_tools"`
}

const sparkSlots = 24

// ForSession computes SessionStats for a session id (a top-level session includes its subagents).
func ForSession(ctx context.Context, st *store.Store, id int64) (SessionStats, error) {
	var out SessionStats
	db := st.DB
	var host, uuid, parent string
	if err := db.QueryRowContext(ctx, `SELECT host, session_uuid, parent_uuid FROM sessions WHERE id = ?`, id).Scan(&host, &uuid, &parent); err != nil {
		return out, err
	}
	ids := []any{id}
	if parent == "" && uuid != "" {
		rows, err := db.QueryContext(ctx, `SELECT id FROM sessions WHERE host = ? AND parent_uuid = ?`, host, uuid)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var sid int64
			if err := rows.Scan(&sid); err != nil {
				rows.Close()
				return out, err
			}
			ids = append(ids, sid)
		}
		rows.Close()
		out.Subagents = len(ids) - 1
	}
	in := "?" + repeat(",?", len(ids)-1)
	type ev struct {
		ts  time.Time
		tok int64
	}
	var evs []ev
	models := map[string]bool{}
	rows, err := db.QueryContext(ctx, `SELECT ts, model, input, output, cache_read, cache_write FROM usage_events WHERE session_id IN (`+in+`)`, ids...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var ts, model string
		var t Tokens
		if err := rows.Scan(&ts, &model, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite); err != nil {
			rows.Close()
			return out, err
		}
		out.Tokens.Add(t)
		out.Dollars += Cost(model, t)
		out.Responses++
		models[model] = true
		e := ev{store.ParseT(ts), t.Total()}
		evs = append(evs, e)
		if out.First.IsZero() || e.ts.Before(out.First) {
			out.First = e.ts
		}
		if e.ts.After(out.Last) {
			out.Last = e.ts
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for m := range models {
		out.Models = append(out.Models, m)
	}
	sort.Strings(out.Models)
	if len(evs) > 0 {
		out.Spark = make([]float64, sparkSlots)
		span := out.Last.Sub(out.First)
		for _, e := range evs {
			i := 0
			if span > 0 {
				i = int(float64(e.ts.Sub(out.First)) / float64(span) * float64(sparkSlots-1))
			}
			out.Spark[max(0, min(sparkSlots-1, i))] += float64(e.tok)
		}
	}
	trows, err := db.QueryContext(ctx, `SELECT tool, COUNT(*), SUM(is_error) FROM tool_calls WHERE session_id IN (`+in+`) GROUP BY tool`, ids...)
	if err != nil {
		return out, err
	}
	for trows.Next() {
		var t ToolStat
		if err := trows.Scan(&t.Tool, &t.Calls, &t.Errors); err != nil {
			trows.Close()
			return out, err
		}
		out.ToolCalls += t.Calls
		out.ToolErrors += t.Errors
		out.TopTools = append(out.TopTools, t)
	}
	trows.Close()
	sort.Slice(out.TopTools, func(i, j int) bool { return out.TopTools[i].Calls > out.TopTools[j].Calls })
	out.TopTools = out.TopTools[:min(len(out.TopTools), 5)]
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(prompts), 0) FROM activity WHERE session_id IN (`+in+`)`, ids...).Scan(&out.Prompts)
	return out, trows.Err()
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
