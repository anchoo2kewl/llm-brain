package insights

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/charts"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// now is the fixture's clock: a Wednesday afternoon, UTC.
var now = time.Date(2026, 3, 11, 18, 30, 0, 0, time.UTC)

func ts(d time.Duration) string { return store.T(now.Add(-d)) }

func exec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.DB.Exec(q, args...); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}

func usageRaw(id, model string, in, out, read, write int) string {
	b, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"id": id, "model": model, "role": "assistant",
		"usage": map[string]any{"input_tokens": in, "output_tokens": out, "cache_read_input_tokens": read, "cache_creation_input_tokens": write}}})
	return string(b)
}

// fixture builds a small hub: two hosts, a session with a subagent, tool calls with an error, gateway traffic
// on a personal and a work account, fleet runs with review verdicts, and chat messages.
func fixture(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// Sessions: 1 (laptop, claude, with subagent 2), 3 (studio, codex).
	exec(t, st, `INSERT INTO sessions (id, host, path, provider, session_uuid, parent_uuid, cwd, title, first_at, last_at, file_mtime, message_count)
		VALUES (1, 'laptop', '/p/1.jsonl', 'claude', 'u-1', '', '/home/dev/acme/web', 'fix the login page', ?, ?, ?, 6),
		       (2, 'laptop', '/p/1/sub.jsonl', 'claude', 'u-2', 'u-1', '/home/dev/acme/web', 'subagent', ?, ?, ?, 2),
		       (3, 'studio', '/p/3.jsonl', 'codex', 'u-3', '', '/home/dev/acme/api', 'add an endpoint', ?, ?, ?, 2)`,
		ts(3*time.Hour), ts(time.Hour), ts(time.Hour), ts(2*time.Hour), ts(2*time.Hour), ts(2*time.Hour),
		ts(30*time.Hour), ts(29*time.Hour), ts(29*time.Hour))
	msg := `INSERT INTO messages (session_id, line_off, idx, role, kind, meta, ts, text, tool_name, tool_use_id, is_error, raw) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	// Session 1: a typed prompt, injected context, a response written as two lines with the same id (the second
	// carries the final output count), two tool calls (one fails).
	exec(t, st, msg, 1, 0, 0, "user", "text", 0, ts(3*time.Hour), "fix the login page", "", "", 0, "")
	exec(t, st, msg, 1, 10, 0, "user", "text", 1, ts(3*time.Hour), "<system-reminder>…", "", "", 0, "")
	exec(t, st, msg, 1, 20, 0, "assistant", "text", 0, ts(170*time.Minute), "Looking.", "", "", 0, usageRaw("m-1", "claude-opus-5-5", 10, 5, 1000, 200))
	exec(t, st, msg, 1, 30, 0, "assistant", "tool_use", 0, ts(170*time.Minute), "", "Bash", "tu-1", 0, usageRaw("m-1", "claude-opus-5-5", 10, 50, 1000, 200))
	exec(t, st, msg, 1, 40, 0, "user", "tool_result", 0, ts(169*time.Minute), "exit 1", "", "tu-1", 1, "")
	exec(t, st, msg, 1, 50, 0, "assistant", "tool_use", 0, ts(160*time.Minute), "", "Read", "tu-2", 0, usageRaw("m-2", "claude-opus-5-5", 0, 100, 2000, 0))
	exec(t, st, msg, 1, 60, 0, "user", "tool_result", 0, ts(159*time.Minute), "ok", "", "tu-2", 0, "")
	// Subagent 2 belongs to session 1.
	exec(t, st, msg, 2, 0, 0, "assistant", "text", 0, ts(2*time.Hour), "done", "", "", 0, usageRaw("m-3", "claude-haiku-5-5", 100, 100, 0, 0))
	// Session 3 (codex, yesterday): a prompt, no usage, a timestamp-less block dated at its session start.
	exec(t, st, msg, 3, 0, 0, "user", "text", 0, ts(30*time.Hour), "add an endpoint", "", "", 0, "")
	exec(t, st, msg, 3, 10, 0, "assistant", "text", 0, "", "ok", "", "", 0, "")

	// Gateway: personal (priority 10) and work (priority 0) Claude accounts.
	exec(t, st, `INSERT INTO gateway_accounts (id, auth_index, provider, email, label, priority, disabled, failed, cooldowns, windows, quota_at, seen_at)
		VALUES ('p.json', 'ai-p', 'claude', 'me@example.com', '', 10, 0, 0, '[]', ?, ?, ?),
		       ('w.json', 'ai-w', 'claude', 'me@acme.example', '', 0, 0, 0, '[]', '[]', '', ?)`,
		fmt.Sprintf(`[{"name":"5h","used_pct":60,"reset_at":%d,"status":"allowed"}]`, now.Add(2*time.Hour).Unix()), ts(time.Minute), ts(time.Minute), ts(time.Minute))
	gw := `INSERT INTO gateway_requests (request_id, ts, auth_index, account, model, input_tokens, output_tokens, cached_tokens, total_tokens,
		latency_ms, failed, status_code, user_agent) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,'')`
	for i := 0; i < 6; i++ {
		exec(t, st, gw, fmt.Sprintf("p%d", i), ts(time.Duration(5+i)*time.Hour), "ai-p", "me@example.com", "claude-opus-5-5", 10, 100, 1000, 1110, 1000*(i+1), 0, 200)
	}
	// Work served two requests at 15:xx while personal was capped, and one failed with a 429.
	exec(t, st, gw, "w1", store.T(time.Date(2026, 3, 11, 15, 10, 0, 0, time.UTC)), "ai-w", "me@acme.example", "claude-opus-5-5", 10, 100, 1000, 1110, 900, 0, 200)
	exec(t, st, gw, "w2", store.T(time.Date(2026, 3, 11, 15, 40, 0, 0, time.UTC)), "ai-w", "me@acme.example", "claude-opus-5-5", 10, 100, 1000, 1110, 900, 0, 200)
	exec(t, st, gw, "w3", store.T(time.Date(2026, 3, 11, 15, 50, 0, 0, time.UTC)), "ai-w", "me@acme.example", "claude-opus-5-5", 0, 0, 0, 0, 0, 1, 429)
	hist := `INSERT INTO gateway_quota_history (account_id, win, at, used_pct, reset_at) VALUES (?,?,?,?,?)`
	exec(t, st, hist, "p.json", "5h", store.T(time.Date(2026, 3, 11, 15, 0, 0, 0, time.UTC)), 100.0, 0)
	exec(t, st, hist, "p.json", "5h", store.T(time.Date(2026, 3, 11, 16, 0, 0, 0, time.UTC)), 20.0, now.Add(2*time.Hour).Unix())
	exec(t, st, hist, "p.json", "5h", store.T(time.Date(2026, 3, 11, 18, 0, 0, 0, time.UTC)), 60.0, now.Add(2*time.Hour).Unix())

	// Fleet: two reviews (approve, comment with an A01 blocker), an issue run that failed, a PR run.
	run := `INSERT INTO runs (host, path, key, kind, target, provider, started_at, ended_at, status, summary) VALUES (?,?,?,?,?,?,?,?,?,?)`
	exec(t, st, run, "studio", "/l/1", "review-web-7", "review", "https://github.com/acme/web/pull/7", "claude", ts(4*time.Hour), ts(230*time.Minute), "succeeded",
		"REVIEWED https://github.com/acme/web/pull/7 APPROVE red=0 optional=1 files=3/3")
	exec(t, st, run, "studio", "/l/2", "review-web-8", "review", "https://github.com/acme/web/pull/8", "claude", ts(3*time.Hour), ts(170*time.Minute), "succeeded",
		"REVIEWED https://github.com/acme/web/pull/8 COMMENT red=2 optional=0 A01 missing tenant check")
	exec(t, st, run, "studio", "/l/3", "issue-12", "issue", "https://github.com/acme/tracker/issues/12", "claude", ts(5*time.Hour), ts(4*time.Hour), "failed", "")
	exec(t, st, run, "studio", "/l/4", "pr-web-8", "pr", "https://github.com/acme/web/pull/8", "claude", ts(2*time.Hour), ts(110*time.Minute), "exited", "")

	// Chat: a thread in #eng with a mention of me, and a DM.
	exec(t, st, `INSERT INTO settings (key, value) VALUES ('slack_user_id', 'U-ME')`)
	chat := `INSERT INTO chat_messages (source, conv_id, conv_name, conv_kind, mentions_me, ts, thread_ts, user_id, user_name, text, at) VALUES ('slack',?,?,?,?,?,?,?,?,'x',?)`
	exec(t, st, chat, "C1", "eng", "channel", 0, "100.1", "100.1", "U-A", "teammate-a", ts(3*time.Hour))
	exec(t, st, chat, "C1", "eng", "channel", 1, "100.2", "100.1", "U-B", "teammate-b", ts(170*time.Minute))
	exec(t, st, chat, "C1", "eng", "channel", 0, "100.3", "100.1", "U-ME", "me", ts(160*time.Minute))
	exec(t, st, chat, "D1", "teammate-a", "dm", 0, "200.1", "", "U-A", "teammate-a", ts(time.Hour))
	return st
}

func rolledUp(t *testing.T) *store.Store {
	st := fixture(t)
	if err := (&Rollup{Store: st, Batch: 3}).CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRollup(t *testing.T) {
	st := rolledUp(t)
	db := st.DB
	var n int
	var out, read int64
	_ = db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&n)
	if n != 3 {
		t.Fatalf("usage_events: want 3 responses (m-1 deduped), got %d", n)
	}
	_ = db.QueryRow(`SELECT output, cache_read FROM usage_events WHERE api_id = 'm-1'`).Scan(&out, &read)
	if out != 50 || read != 1000 {
		t.Fatalf("m-1 keeps the largest counts once: output=%d cache_read=%d", out, read)
	}
	var errs int
	_ = db.QueryRow(`SELECT COUNT(*), SUM(is_error) FROM tool_calls`).Scan(&n, &errs)
	if n != 2 || errs != 1 {
		t.Fatalf("tool_calls: %d calls, %d errors", n, errs)
	}
	var prompts, blocks int64
	_ = db.QueryRow(`SELECT SUM(prompts), SUM(blocks) FROM activity`).Scan(&prompts, &blocks)
	if prompts != 2 || blocks != 10 {
		t.Fatalf("activity: prompts=%d (injected context is not a prompt), blocks=%d", prompts, blocks)
	}
	if p := Progress(context.Background(), st); p != 100 {
		t.Fatalf("progress %d", p)
	}
	// A second pass is a no-op.
	if k, _ := (&Rollup{Store: st}).Step(context.Background()); k != 0 {
		t.Fatal("caught-up rollup should do nothing")
	}
	// New messages are picked up incrementally.
	exec(t, st, `INSERT INTO messages (session_id, line_off, idx, role, kind, ts) VALUES (1, 70, 0, 'user', 'text', ?)`, ts(time.Minute))
	if err := (&Rollup{Store: st}).CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT SUM(prompts) FROM activity`).Scan(&prompts)
	if prompts != 3 {
		t.Fatalf("incremental prompt not counted: %d", prompts)
	}
}

func TestBuildReport(t *testing.T) {
	st := rolledUp(t)
	r, err := Build(context.Background(), st, ParseRange("24h"), now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Labels) != 24 || r.buckets.Step != time.Hour {
		t.Fatalf("24h is 24 hourly buckets, got %d", len(r.Labels))
	}
	// Transcript usage: m-1 (10+50+1000+200) + m-2 (100+2000) + m-3 (200).
	if got := r.LLM.Totals.Total(); got != 1260+2100+200 {
		t.Fatalf("tokens %d", got)
	}
	if r.Cost.Total <= 0 || len(r.Cost.Unpriced) != 0 {
		t.Fatalf("cost %v unpriced %v", r.Cost.Total, r.Cost.Unpriced)
	}
	if len(r.Sessions.Costliest) != 1 || r.Sessions.Costliest[0].ID != 1 {
		t.Fatalf("the subagent's cost belongs to session 1: %+v", r.Sessions.Costliest)
	}
	if r.Sessions.Started != 1 || r.Sessions.Prompts != 1 {
		t.Fatalf("24h: 1 session started (codex one was 30h ago), 1 prompt; got %d, %d", r.Sessions.Started, r.Sessions.Prompts)
	}
	if r.Sessions.ToolCalls != 2 || r.Sessions.ToolErrors != 1 {
		t.Fatalf("tools %d/%d", r.Sessions.ToolCalls, r.Sessions.ToolErrors)
	}
	if r.Sessions.HeatPeak != "Wed 15:00" {
		t.Fatalf("heat peak %q", r.Sessions.HeatPeak)
	}
	// Gateway
	l := r.LLM
	if l.GwRequests != 9 || l.GwFailed != 1 || l.GwRateLimited != 1 || l.WorkRequests != 3 {
		t.Fatalf("gateway: %+v", l)
	}
	if l.WorkWhileCapped != 3 {
		t.Fatalf("all 3 work requests came while personal 5h was at 100%% (15:00-16:00), got %d", l.WorkWhileCapped)
	}
	if l.WorkHours[15] != 3 {
		t.Fatalf("work hours %v", l.WorkHours)
	}
	if l.WorkTokenPct < 24 || l.WorkTokenPct > 26 {
		t.Fatalf("work share %.1f%% (2 of 8 equal-size successful requests)", l.WorkTokenPct)
	}
	var opus ModelLatency
	for _, m := range l.Latency {
		if m.Model == "claude-opus-5-5" {
			opus = m
		}
	}
	if opus.N != 8 || opus.P95 != 6000 || opus.P50 != 2000 {
		t.Fatalf("latency %+v", opus)
	}
	// Quota: 60% used, history in this window shows 40 points over 2h = 20%/h, so 40% left lasts 2h, which is
	// exactly the reset: "lasts until reset".
	var q QuotaView
	for _, x := range r.Quota {
		if x.Class == "personal" && x.Window == "5h" {
			q = x
		}
	}
	if q.BurnFrom != "history" || q.BurnPerHour != 20 || !q.HasHistory || q.Verdict != "lasts until reset at this rate" {
		t.Fatalf("quota %+v", q)
	}
	// Fleet
	f := r.Fleet
	if f.Runs != 4 || f.Approve != 1 || f.Comment != 1 || f.Red != 2 || f.Optional != 1 || f.Failed != 1 {
		t.Fatalf("fleet %+v", f)
	}
	if len(f.Blockers) != 1 || f.Blockers[0].Label != "A01 access control" {
		t.Fatalf("blockers %+v", f.Blockers)
	}
	if len(f.Repos) == 0 || f.Repos[0].Label != "acme/web" || f.Repos[0].Value != 3 {
		t.Fatalf("repos %+v", f.Repos)
	}
	// Chat
	c := r.Chat
	if c.Total != 4 || c.Mentions != 1 || c.DMs != 1 || len(c.Threads) != 1 || c.Threads[0].Replies != 3 || c.Threads[0].People != 3 {
		t.Fatalf("chat %+v", c)
	}
	// Previous-period comparisons exist for bounded ranges.
	if !r.KPIs[0].HasPrev {
		t.Fatal("24h KPIs compare with the previous 24h")
	}
	// 7d sees the codex session too, in 6-hour buckets.
	w, err := Build(context.Background(), st, ParseRange("7d"), now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if w.Sessions.Started != 2 || len(w.Labels) != 28 {
		t.Fatalf("7d: started %d, buckets %d", w.Sessions.Started, len(w.Labels))
	}
	all, err := Build(context.Background(), st, ParseRange("all"), now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if all.KPIs[0].HasPrev {
		t.Fatal("all-time has no previous period")
	}
}

func TestCache(t *testing.T) {
	st := rolledUp(t)
	c := &Cache{Store: st, Now: func() time.Time { return now }, Loc: time.UTC}
	a, err := c.Get(context.Background(), ParseRange("24h"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := c.Get(context.Background(), ParseRange("24h"))
	if a != b {
		t.Fatal("second Get within TTL should be cached")
	}
	c.Invalidate()
	if x, _ := c.Get(context.Background(), ParseRange("24h")); x == a {
		t.Fatal("Invalidate should drop the report")
	}
}

func TestForSession(t *testing.T) {
	st := rolledUp(t)
	s, err := ForSession(context.Background(), st, 1)
	if err != nil {
		t.Fatal(err)
	}
	if s.Subagents != 1 || s.Responses != 3 || s.Tokens.Total() != 3560 || s.ToolCalls != 2 || s.ToolErrors != 1 || s.Prompts != 1 {
		t.Fatalf("%+v", s)
	}
	if len(s.Spark) != sparkSlots || len(s.Models) != 2 {
		t.Fatalf("spark %d models %v", len(s.Spark), s.Models)
	}
}

func TestBucketsBoundedAreExactWindows(t *testing.T) {
	loc := time.FixedZone("X", -4*3600)
	n := time.Date(2026, 3, 11, 13, 20, 0, 0, loc) // Wednesday
	for _, c := range []struct {
		key  string
		n    int
		step time.Duration
	}{{"24h", 24, time.Hour}, {"7d", 28, 6 * time.Hour}, {"30d", 30, 24 * time.Hour}} {
		r := ParseRange(c.key)
		b := NewBuckets(r, n, time.Time{}, loc)
		if b.N != c.n || b.Step != c.step {
			t.Fatalf("%s: %d buckets of %v, want %d of %v", c.key, b.N, b.Step, c.n, c.step)
		}
		// The window is exactly the range, ending now, and the buckets tile it exactly.
		if d := b.To.Sub(b.From); d != r.Dur || b.To.Sub(n) > time.Millisecond || n.Before(b.From) {
			t.Fatalf("%s: window %v..%v (%v)", c.key, b.From, b.To, d)
		}
		if !b.BucketStart(b.N).Equal(b.To) {
			t.Fatalf("%s: buckets end at %v, window at %v", c.key, b.BucketStart(b.N), b.To)
		}
		if b.Index(n) != b.N-1 || b.Index(b.From) != 0 || b.Index(b.From.Add(-time.Millisecond)) != -1 || b.Index(b.To) != -1 {
			t.Fatalf("%s: index edges", c.key)
		}
		p := b.Prev()
		if !p.To.Equal(b.From) || p.To.Sub(p.From) != r.Dur {
			t.Fatalf("%s: previous window %v..%v", c.key, p.From, p.To)
		}
		ax := b.Axis()
		if len(ax.Labels) != b.N || len(ax.Tips) != b.N {
			t.Fatalf("%s: axis", c.key)
		}
	}
}

func TestBucketsAllTimeAlignsTheEarliestBucket(t *testing.T) {
	loc := time.UTC
	n := time.Date(2026, 3, 11, 13, 20, 0, 0, loc) // Wednesday
	// Earliest datum on a Sunday a year back: its week (from the Monday before) must be the first bucket.
	first := time.Date(2025, 3, 9, 23, 0, 0, 0, loc) // Sunday
	a := NewBuckets(ParseRange("all"), n, first, loc)
	if a.Days != 7 || !a.From.Equal(time.Date(2025, 3, 3, 0, 0, 0, 0, loc)) {
		t.Fatalf("weekly from the Monday of the earliest week, got %v", a.From)
	}
	if a.Index(first) != 0 || a.Index(n) != a.N-1 || !a.BucketStart(a.N-1).Equal(time.Date(2026, 3, 9, 0, 0, 0, 0, loc)) {
		t.Fatalf("earliest in bucket 0 (%d), now in the last (%d of %d)", a.Index(first), a.Index(n), a.N)
	}
	// A short history is hourly from the earliest hour, not a 7-day default.
	h := NewBuckets(ParseRange("all"), n, n.Add(-150*time.Minute), loc)
	if h.Step != time.Hour || h.N != 4 || !h.From.Equal(time.Date(2026, 3, 11, 10, 0, 0, 0, loc)) {
		t.Fatalf("hourly all-time: %+v", h)
	}
	// Daily: the earliest day is bucket 0.
	d := NewBuckets(ParseRange("all"), n, time.Date(2026, 2, 1, 22, 0, 0, 0, loc), loc)
	if d.Days != 1 || d.N != 39 || d.Index(time.Date(2026, 2, 1, 22, 0, 0, 0, loc)) != 0 {
		t.Fatalf("daily all-time: %+v", d)
	}
	if ParseRange("bogus").Key != "7d" {
		t.Fatal("default range")
	}
}

func TestReportTotalsUseExactWindows(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run := `INSERT INTO runs (host, path, key, kind, started_at, status) VALUES ('h', ?, 'issue-1', 'issue', ?, 'succeeded')`
	// Inside the 24h window by a minute, outside by a minute, and inside the previous window.
	exec(t, st, run, "/a", ts(24*time.Hour-time.Minute))
	exec(t, st, run, "/b", ts(24*time.Hour+time.Minute))
	exec(t, st, run, "/c", ts(47*time.Hour))
	exec(t, st, run, "/d", ts(49*time.Hour))
	r, err := Build(context.Background(), st, ParseRange("24h"), now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if r.Fleet.Runs != 1 || r.Fleet.PrevRuns != 2 {
		t.Fatalf("24h counts runs in [now-24h, now) = 1 and [now-48h, now-24h) = 2; got %d and %d", r.Fleet.Runs, r.Fleet.PrevRuns)
	}
}

func TestAllTimeStartsAtTheEarliestSource(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// No sessions at all: a fleet run 20 days ago and a chat message 40 days ago.
	exec(t, st, `INSERT INTO runs (host, path, key, kind, started_at, status) VALUES ('h', '/a', 'issue-1', 'issue', ?, 'succeeded')`, ts(20*24*time.Hour))
	exec(t, st, `INSERT INTO chat_messages (source, conv_id, conv_name, conv_kind, ts, text, at) VALUES ('slack', 'C1', 'eng', 'channel', '1.1', 'x', ?)`,
		ts(40*24*time.Hour))
	if e := Earliest(context.Background(), st.DB); !e.Equal(now.Add(-40 * 24 * time.Hour).Truncate(time.Millisecond)) {
		t.Fatalf("earliest %v", e)
	}
	r, err := Build(context.Background(), st, ParseRange("all"), now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if r.buckets.Days != 1 || r.From.After(now.Add(-40*24*time.Hour)) || r.Fleet.Runs != 1 || r.Chat.Total != 1 {
		t.Fatalf("all time covers 40 days of daily buckets from the chat message: from %v, runs %d, chat %d", r.From, r.Fleet.Runs, r.Chat.Total)
	}
}

func TestCapSpansCloseAtReset(t *testing.T) {
	at := func(h int) time.Time { return now.Add(time.Duration(h) * time.Hour) }
	reset := at(2).Unix()
	// Capped at 0h with a reset at 2h; the next observation (uncapped) only comes at 5h.
	sp := capSpans([]capPoint{{at(0), 100, reset}, {at(1), 100, reset}, {at(5), 10, at(10).Unix()}})
	if len(sp) != 1 || !sp[0].from.Equal(at(0)) || !sp[0].to.Equal(at(2)) {
		t.Fatalf("closes at the reset, not at the next observation: %+v", sp)
	}
	// Uncapped before the reset closes it there.
	sp = capSpans([]capPoint{{at(0), 97, reset}, {at(1), 30, reset}})
	if len(sp) != 1 || !sp[0].to.Equal(at(1)) {
		t.Fatalf("%+v", sp)
	}
	// Still capped at the last observation: open until the reset; with no reset known, open-ended.
	if sp = capSpans([]capPoint{{at(0), 100, reset}}); !sp[0].to.Equal(at(2)) {
		t.Fatalf("%+v", sp)
	}
	if sp = capSpans([]capPoint{{at(0), 100, 0}}); !sp[0].to.IsZero() {
		t.Fatalf("%+v", sp)
	}
	// Capped again in the next window: a second interval.
	sp = capSpans([]capPoint{{at(0), 100, reset}, {at(3), 100, at(8).Unix()}, {at(4), 50, at(8).Unix()}})
	if len(sp) != 2 || !sp[0].to.Equal(at(2)) || !sp[1].from.Equal(at(3)) || !sp[1].to.Equal(at(4)) {
		t.Fatalf("%+v", sp)
	}
}

func TestPrices(t *testing.T) {
	if p, ok := PriceFor("claude-opus-5-5"); !ok || p.Input != 4 {
		t.Fatalf("opus 5.5: %+v", p)
	}
	if p, _ := PriceFor("claude-opus-4-8"); p.Input != 5 {
		t.Fatal("longest prefix wins")
	}
	if _, ok := PriceFor("mystery-1"); ok {
		t.Fatal("unknown models are unpriced")
	}
	if c := Cost("claude-sonnet-5-5", Tokens{Input: 1e6, Output: 1e6}); c != 12 {
		t.Fatalf("sonnet 1M in + 1M out = $12, got %v", c)
	}
	// Claude-style: cached apart from input, the rest of total is cache writes.
	if g := GatewayTokens("claude-x", 10, 100, 1000, 1500); g.CacheWrite != 390 || g.Input != 10 {
		t.Fatalf("%+v", g)
	}
	// OpenAI-style: cached inside input.
	if g := GatewayTokens("gpt-x", 1000, 10, 800, 1010); g.Input != 200 || g.CacheRead != 800 {
		t.Fatalf("%+v", g)
	}
}

// ---------------------------------------------------------------- callout rules

func base() *Report {
	return &Report{Range: ParseRange("7d"), RollupPct: 100}
}

func find(cs []Callout, id string) *Callout {
	for i := range cs {
		if strings.HasPrefix(cs[i].ID, id) {
			return &cs[i]
		}
	}
	return nil
}

func TestCalloutWorkShare(t *testing.T) {
	r := base()
	r.LLM.GwRequests, r.LLM.WorkRequests, r.LLM.WorkWhileCapped, r.LLM.WorkTokenPct = 100, 12, 12, 18
	r.LLM.WorkHours = make([]float64, 24)
	r.LLM.WorkHours[14], r.LLM.WorkHours[16] = 5, 7
	c := find(Callouts(r), "work-share")
	if c == nil || c.Text != "Work accounts served 18% of gateway tokens in the last 7 days (12 requests), all between 14:00 and 17:00, every one while a personal window was at its cap." || c.Href != "#share" {
		t.Fatalf("%+v", c)
	}
	r.LLM.WorkRequests = 0
	if c := find(Callouts(r), "work-share"); c == nil || c.Level != "good" || !strings.Contains(c.Text, "work accounts were not needed") {
		t.Fatalf("%+v", c)
	}
}

func TestCalloutReviews(t *testing.T) {
	r := base()
	r.Fleet.Approve, r.Fleet.Comment = 8, 2
	r.Fleet.Blockers = blockerBars("A01 access control", "A03 injection")
	c := find(Callouts(r), "reviews")
	if c == nil || c.Text != "PR reviews approve 80% (8 of 10) in the last 7 days; the most common blocker is A01 access control." {
		t.Fatalf("%+v", c)
	}
}

func TestCalloutLatency(t *testing.T) {
	r := base()
	r.LLM.Latency = []ModelLatency{{Model: "codex", N: 20, P95: 8000, PrevN: 20, PrevP95: 4000}, {Model: "steady", N: 20, P95: 1000, PrevN: 20, PrevP95: 1000}}
	cs := Callouts(r)
	c := find(cs, "latency-codex")
	if c == nil || c.Level != "warn" || c.Text != "codex latency p95 rose 2.0× vs the previous 7 days (4.0s → 8.0s)." {
		t.Fatalf("%+v", c)
	}
	if find(cs, "latency-steady") != nil {
		t.Fatal("no finding for steady latency")
	}
}

func TestCalloutQuotaAndOrder(t *testing.T) {
	r := base()
	soon := time.Now().Add(90 * time.Minute)
	r.Quota = []QuotaView{
		{ID: "q-a", Provider: "claude", Class: "personal", Window: "5h", UsedPct: 85, BurnPerHour: 10, RunsOutAt: soon, ResetAt: soon.Add(time.Hour)},
		{ID: "q-b", Provider: "codex", Class: "personal", Window: "7d", UsedPct: 100, Exhausted: true},
	}
	r.Cost.Total = 42
	cs := Callouts(r)
	if cs[0].Level != "bad" || cs[len(cs)-1].Level == "bad" {
		t.Fatalf("most urgent first: %+v", cs)
	}
	a := find(cs, "quota-q-a")
	if a == nil || a.Level != "bad" || !strings.Contains(a.Text, "runs out around") || a.Href != "#q-a" {
		t.Fatalf("%+v", a)
	}
	if b := find(cs, "quota-q-b"); b == nil || !strings.Contains(b.Text, "is exhausted") {
		t.Fatalf("%+v", b)
	}
	if v := find(cs, "value"); v == nil || v.Text != "Subscriptions delivered about $42.00 of API-equivalent usage in the last 7 days (estimate)." {
		t.Fatalf("%+v", v)
	}
}

func TestCalloutThresholds(t *testing.T) {
	r := base()
	r.Sessions.Tools = []ToolStat{{Tool: "rare", Calls: 5, Errors: 5}, {Tool: "flaky", Calls: 40, Errors: 10}}
	r.LLM.GwRequests, r.LLM.GwFailed, r.LLM.GwRateLimited = 50, 5, 4
	r.LLM.PrevTotal = 100
	r.LLM.Totals = Tokens{Input: 400}
	r.RollupPct = 40
	cs := Callouts(r)
	if c := find(cs, "tool-errors"); c == nil || !strings.HasPrefix(c.Text, "flaky calls fail 25%") {
		t.Fatalf("tools need 20+ calls: %+v", c)
	}
	if c := find(cs, "gw-errors"); c == nil || !strings.Contains(c.Text, "4 were 429 rate limits") {
		t.Fatalf("%+v", c)
	}
	if c := find(cs, "token-trend"); c == nil || c.Text != "Token use is 4× the previous 7 days." {
		t.Fatalf("%+v", c)
	}
	if find(cs, "rollup") == nil {
		t.Fatal("partial history is called out")
	}
	if len(Callouts(base())) != 0 {
		t.Fatal("an empty report has nothing to say")
	}
}

func blockerBars(labels ...string) []charts.Bar {
	out := make([]charts.Bar, len(labels))
	for i, l := range labels {
		out[i] = charts.Bar{Label: l, Value: float64(len(labels) - i)}
	}
	return out
}
