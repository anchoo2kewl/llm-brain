package ingest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func newEngine(t *testing.T) *Engine {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return NewEngine(s, "acme", "acme/web-app")
}

func count(t *testing.T, e *Engine, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.S.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func userLine(sess, text string, ts int) string {
	return fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":"/w","timestamp":"2026-10-14T20:00:%02dZ","message":{"role":"user","content":%q}}`+"\n", sess, ts, text)
}

func appendFile(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// fixture lays out a machine: one Claude transcript, one subagent, one memory file to skip, one worker log,
// fleet.log, a prompt and a live pid.
func fixture(t *testing.T) (home string, claudeFile string) {
	home = t.TempDir()
	proj := filepath.Join(home, "claude", "-Users-a-w")
	must(t, os.MkdirAll(filepath.Join(proj, "s1", "subagents"), 0o755))
	must(t, os.MkdirAll(filepath.Join(proj, "memory"), 0o755))
	claudeFile = filepath.Join(proj, "s1.jsonl")
	appendFile(t, claudeFile, userLine("s1", "first prompt", 1)+
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-10-14T20:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`+"\n"+
		`{"type":"user","sessionId":"s1","timestamp":"2026-10-14T20:00:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a.txt"}]}}`+"\n"+
		`{"type":"agent-name","agentName":"fleet-review-web-app-app2-839","sessionId":"s1"}`+"\n")
	appendFile(t, filepath.Join(proj, "s1", "subagents", "agent-x.jsonl"), userLine("s1", "sub task", 4))
	appendFile(t, filepath.Join(proj, "memory", "notes.jsonl"), userLine("m", "skip me", 5))
	state := filepath.Join(home, "state")
	must(t, os.MkdirAll(filepath.Join(state, "logs"), 0o755))
	must(t, os.MkdirAll(filepath.Join(state, "runs"), 0o755))
	appendFile(t, filepath.Join(state, "logs", "review-web-app-app2-839.20261014-160000.log"), "working\nREVIEWED https://github.com/acme/web-app-app2/pull/839 APPROVE 1 inline\n")
	appendFile(t, filepath.Join(state, "logs", "fleet.log"), "2026-10-14 16:00:00 [h] tick\nstray stderr line\n")
	appendFile(t, filepath.Join(state, "logs", "hub.log"), "not a worker log\n")
	appendFile(t, filepath.Join(state, "logs", "issue-5.20261014-160500.log"), "") // a claude -p worker prints nothing until it ends
	appendFile(t, filepath.Join(state, "runs", "issue-5.pid"), fmt.Sprint(os.Getpid()))
	appendFile(t, filepath.Join(state, "runs", "review-web-app-app2-839.prompt"), "Review the PR")
	return home, claudeFile
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func collector(home string, sink Sink) *Collector {
	return &Collector{Host: "laptop", Role: "test", ClaudeDir: filepath.Join(home, "claude"), StateDir: filepath.Join(home, "state"),
		TZ: "America/Toronto", Sink: sink, ChunkMax: 64}
}

func TestCollectorIdempotentAndIncremental(t *testing.T) {
	e := newEngine(t)
	home, cf := fixture(t)
	c := collector(home, e)
	ctx := context.Background()
	must(t, c.Cycle(ctx))

	if n := count(t, e, `SELECT count(*) FROM sessions`); n != 2 {
		t.Fatalf("sessions = %d, want 2 (main + subagent, memory skipped)", n)
	}
	if n := count(t, e, `SELECT count(*) FROM messages`); n != 5 {
		t.Fatalf("messages = %d, want 5", n)
	}
	if n := count(t, e, `SELECT count(*) FROM events`); n != 2 {
		t.Fatalf("events = %d", n)
	}
	if n := count(t, e, `SELECT count(*) FROM runs WHERE status = 'succeeded' AND prompt = 'Review the PR' AND session_id IS NOT NULL
		AND target = 'https://github.com/acme/web-app-app2/pull/839'`); n != 1 {
		var st, pr, tg string
		var sid any
		_ = e.S.DB.QueryRow(`SELECT status, prompt, target, session_id FROM runs`).Scan(&st, &pr, &tg, &sid)
		t.Fatalf("review run should be succeeded, have its prompt, and be linked to the fleet-named session: %q %q %q %v", st, pr, tg, sid)
	}
	if n := count(t, e, `SELECT count(*) FROM runs WHERE key = 'issue-5' AND status = 'running' AND ended_at = '' AND log = ''`); n != 1 {
		t.Fatal("an empty worker log with a live pid must be a running run")
	}
	if n := count(t, e, `SELECT count(*) FROM sessions WHERE parent_uuid = 's1' AND project = '-Users-a-w'`); n != 1 {
		t.Fatal("subagent parent/project")
	}

	// A second pass and a fresh collector (restart) change nothing.
	must(t, c.Cycle(ctx))
	must(t, collector(home, e).Cycle(ctx))
	if n := count(t, e, `SELECT count(*) FROM messages`); n != 5 {
		t.Fatalf("after re-scan messages = %d, want 5", n)
	}
	if n := count(t, e, `SELECT count(*) FROM events`); n != 2 {
		t.Fatalf("after re-scan events = %d", n)
	}
	var logText string
	must(t, e.S.DB.QueryRow(`SELECT log FROM runs WHERE kind = 'review'`).Scan(&logText))
	if strings.Count(logText, "REVIEWED") != 1 {
		t.Fatalf("log duplicated: %q", logText)
	}

	// A half-written line is left for later; once finished it is read exactly once.
	full := userLine("s1", "second prompt that is long enough to span chunks", 9)
	appendFile(t, cf, full[:30])
	must(t, c.Cycle(ctx))
	if n := count(t, e, `SELECT count(*) FROM messages`); n != 5 {
		t.Fatalf("partial line ingested: %d", n)
	}
	appendFile(t, cf, full[30:])
	must(t, c.Cycle(ctx))
	if n := count(t, e, `SELECT count(*) FROM messages`); n != 6 {
		t.Fatalf("messages = %d, want 6", n)
	}
	if n := count(t, e, `SELECT message_count FROM sessions WHERE session_uuid = 's1' AND parent_uuid = ''`); n != 5 {
		t.Fatalf("message_count = %d", n)
	}
	var title, last string
	must(t, e.S.DB.QueryRow(`SELECT title, last_at FROM sessions WHERE session_uuid = 's1' AND parent_uuid = ''`).Scan(&title, &last))
	if title != "first prompt" || last != "2026-10-14T20:00:09.000Z" {
		t.Fatalf("title %q last %q", title, last)
	}
}

func TestChunkOffsetMismatchAndReplay(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	data := []byte(userLine("s", "one", 1) + userLine("s", "two", 2))
	c := Chunk{Host: "h", Kind: KindClaude, Path: "/p/x/s.jsonl", Offset: 0, Size: int64(len(data)), Mtime: time.Now(), Data: data, EOF: true}
	off, err := e.Chunk(ctx, c)
	if err != nil || off != int64(len(data)) {
		t.Fatalf("off %d err %v", off, err)
	}
	// The same chunk again (a retry after a lost response) is refused with the real offset.
	off2, err := e.Chunk(ctx, c)
	if !errors.Is(err, ErrOffset) || off2 != off {
		t.Fatalf("replay: off %d err %v", off2, err)
	}
	if n := count(t, e, `SELECT count(*) FROM messages`); n != 2 {
		t.Fatalf("messages = %d", n)
	}
	// Truncated file: reset forgets the rows and starts over.
	c2 := Chunk{Host: "h", Kind: KindClaude, Path: "/p/x/s.jsonl", Offset: 0, Reset: true, Data: []byte(userLine("s", "new", 3))}
	if _, err := e.Chunk(ctx, c2); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e, `SELECT count(*) FROM messages`); n != 1 {
		t.Fatalf("after reset messages = %d", n)
	}
}

func TestRunLifecycle(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	p := "/s/logs/issue-17442.20261014-160000.log"
	if _, err := e.Chunk(ctx, Chunk{Host: "h", Kind: KindRunLog, Path: p, TZ: "America/Toronto", Mtime: time.Now(), Data: []byte("step 1\n")}); err != nil {
		t.Fatal(err)
	}
	must(t, e.Heartbeat(ctx, Heartbeat{Host: "h", Running: []string{"issue-17442"}}))
	var status, ended, target, kind string
	must(t, e.S.DB.QueryRow(`SELECT status, ended_at, target, kind FROM runs`).Scan(&status, &ended, &target, &kind))
	if status != "running" || ended != "" || target != "https://github.com/acme/web-app/issues/17442" || kind != "issue" {
		t.Fatalf("%s %s %s %s", status, ended, target, kind)
	}
	if _, err := e.Chunk(ctx, Chunk{Host: "h", Kind: KindRunLog, Path: p, Offset: 7, Mtime: time.Now(), Data: []byte("opened PR, done\n")}); err != nil {
		t.Fatal(err)
	}
	must(t, e.Heartbeat(ctx, Heartbeat{Host: "h"}))
	var summary, logText string
	must(t, e.S.DB.QueryRow(`SELECT status, ended_at, summary, log FROM runs`).Scan(&status, &ended, &summary, &logText))
	if status != "exited" || ended == "" || summary != "opened PR, done" || logText != "step 1\nopened PR, done\n" {
		t.Fatalf("%s %q %q %q", status, ended, summary, logText)
	}
	// A review log with no REVIEWED line failed.
	if _, err := e.Chunk(ctx, Chunk{Host: "h", Kind: KindRunLog, Path: "/s/logs/review-x-1.20261014-160000.log", Mtime: time.Now(), Data: []byte("error: boom\n")}); err != nil {
		t.Fatal(err)
	}
	must(t, e.Heartbeat(ctx, Heartbeat{Host: "h"}))
	must(t, e.S.DB.QueryRow(`SELECT status FROM runs WHERE key = 'review-x-1'`).Scan(&status))
	if status != "failed" {
		t.Fatalf("review status %s", status)
	}
}

func TestRunLogSplitRune(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	p := "/s/logs/pr-r-1.20261014-160000.log"
	b := []byte("ok\nhéllo")
	quiet := time.Now().Add(-time.Minute)
	// Live log: only whole lines.
	off, err := e.Chunk(ctx, Chunk{Host: "h", Kind: KindRunLog, Path: p, Data: b[:5], EOF: true, Mtime: time.Now()})
	if err != nil || off != 3 {
		t.Fatalf("off %d err %v", off, err)
	}
	// Quiet log without a final newline: the tail is taken, but never half a rune ("h" + first byte of é).
	off, err = e.Chunk(ctx, Chunk{Host: "h", Kind: KindRunLog, Path: p, Offset: 3, Data: b[3:5], EOF: true, Mtime: quiet})
	if err != nil || off != 4 {
		t.Fatalf("off %d err %v", off, err)
	}
	if _, err := e.Chunk(ctx, Chunk{Host: "h", Kind: KindRunLog, Path: p, Offset: 4, Data: b[4:], EOF: true, Mtime: quiet}); err != nil {
		t.Fatal(err)
	}
	var logText string
	must(t, e.S.DB.QueryRow(`SELECT log FROM runs`).Scan(&logText))
	if logText != "ok\nhéllo" {
		t.Fatalf("%q", logText)
	}
}

// The shipper over HTTP lands the same rows, under the shipping host's name, and is idempotent too.
func TestShipOverHTTP(t *testing.T) {
	e := newEngine(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ship/offsets", func(w http.ResponseWriter, r *http.Request) {
		m, _ := e.Offsets(r.Context(), r.URL.Query().Get("host"))
		fmt.Fprint(w, "{")
		i := 0
		for k, v := range m {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, "%q:%d", k, v)
			i++
		}
		fmt.Fprint(w, "}")
	})
	mux.HandleFunc("POST /api/ship/chunk", func(w http.ResponseWriter, r *http.Request) {
		c, err := DecodeChunk(r, 1<<20)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		off, err := e.Chunk(r.Context(), c)
		if errors.Is(err, ErrOffset) {
			w.WriteHeader(409)
		} else if err != nil {
			w.WriteHeader(500)
		}
		fmt.Fprintf(w, `{"offset":%d}`, off)
	})
	mux.HandleFunc("POST /api/ship/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	home, _ := fixture(t)
	c := collector(home, NewHTTPSink(ts.URL))
	ctx := context.Background()
	must(t, c.Cycle(ctx))
	must(t, collector(home, NewHTTPSink(ts.URL)).Cycle(ctx))
	if n := count(t, e, `SELECT count(*) FROM messages m JOIN sessions s ON s.id = m.session_id WHERE s.host = 'laptop'`); n != 5 {
		t.Fatalf("shipped messages = %d, want 5", n)
	}
}

func TestQuotaObservations(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	for i, sample := range []struct {
		ts   string
		used int
		host string
	}{
		{"2026-10-14T20:00:02Z", 42, "studio"},
		{"2026-10-14T20:00:01Z", 5, "studio"}, // late arrival must not replace newer quota
		{"2026-10-14T20:00:03Z", 9, "laptop"},
	} {
		line := fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","plan_type":"pro","primary":{"used_percent":%d,"window_minutes":300,"resets_at":1792595789},"secondary":null,"private":"do-not-store"}}}`+"\n", sample.ts, sample.used)
		if _, err := e.Chunk(ctx, Chunk{Host: sample.host, Kind: KindCodex, Path: fmt.Sprintf("/s/%d.jsonl", i), Mtime: time.Now(), Data: []byte(line)}); err != nil {
			t.Fatal(err)
		}
	}
	qs, err := e.S.Quotas(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 {
		t.Fatalf("observations: %+v", qs)
	}
	for _, q := range qs {
		want := float64(42)
		if q.Host == "laptop" {
			want = 9
		}
		if q.Primary == nil || q.Primary.Used != want || q.Secondary != nil {
			t.Fatalf("quota: %+v", q)
		}
	}
	if count(t, e, `SELECT count(*) FROM quota_observations WHERE payload LIKE '%do-not-store%'`) != 0 {
		t.Fatal("unexpected fields persisted")
	}
	if count(t, e, `SELECT count(*) FROM messages`) != 0 {
		t.Fatal("quota should not add transcript blocks")
	}
}
