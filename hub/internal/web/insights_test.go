package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/ingest"
	"github.com/anchoo2kewl/llm-brain/hub/internal/insights"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func insightsServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := ingest.NewEngine(st, "acme", "acme/tracker")
	at := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	lines := `{"type":"user","sessionId":"s","timestamp":"` + at + `","cwd":"/home/dev/acme/web","message":{"role":"user","content":"<b>fix</b> the build"}}` + "\n" +
		`{"type":"assistant","sessionId":"s","timestamp":"` + at + `","cwd":"/home/dev/acme/web","message":{"id":"m1","role":"assistant","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"make"}}],"usage":{"input_tokens":5,"output_tokens":50,"cache_read_input_tokens":500,"cache_creation_input_tokens":0}}}` + "\n"
	if _, err := eng.Chunk(context.Background(), ingest.Chunk{Host: "laptop", Kind: ingest.KindClaude, Path: "/p/x/s.jsonl", Mtime: time.Now(), Data: []byte(lines)}); err != nil {
		t.Fatal(err)
	}
	if err := (&insights.Rollup{Store: st}).CatchUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, eng, t.TempDir(), "laptop", log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func iget(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestInsightsPage(t *testing.T) {
	ts, st := insightsServer(t)
	for _, rg := range []string{"", "?range=24h", "?range=30d", "?range=all", "?range=bogus"} {
		code, body := iget(t, ts.URL+"/insights"+rg)
		if code != 200 {
			t.Fatalf("%s: %d %s", rg, code, body)
		}
		for _, want := range []string{`<svg class="chart`, `id="llm"`, `id="quota"`, `id="cost"`, `id="sessions"`, `id="fleet"`, `id="chat"`, `class="kpi"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %s", rg, want)
			}
		}
		if strings.Contains(body, "style=") {
			t.Errorf("%s: inline style attributes break the CSP", rg)
		}
	}
	// Session pages carry the stats strip.
	if _, body := iget(t, ts.URL+"/sessions/1"); !strings.Contains(body, `class="strip"`) || !strings.Contains(body, "tool calls") {
		t.Errorf("session page has no stats strip")
	}
	// Run pages: stats strip from the run's conversation, and a long log split into earlier + last lines.
	var lg strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&lg, "step %d\n", i)
	}
	if _, err := st.DB.Exec(`INSERT INTO runs (host, path, key, kind, started_at, ended_at, status, log, log_bytes, prompt, session_id)
		VALUES ('laptop', '/l/issue-1.log', 'issue-1', 'issue', ?, ?, 'succeeded', ?, ?, 'do it', 1)`,
		store.Now(), store.Now(), lg.String(), lg.Len()); err != nil {
		t.Fatal(err)
	}
	_, body := iget(t, ts.URL+"/runs/1")
	for _, want := range []string{`class="strip"`, `id="runlog"`, "Earlier log", "step 299", "Prompt"} {
		if !strings.Contains(body, want) {
			t.Errorf("run page missing %q", want)
		}
	}
	// The overview shows KPI tiles.
	if _, body := iget(t, ts.URL+"/fleet"); !strings.Contains(body, `class="kpi"`) {
		t.Error("overview has no KPI tiles")
	}
	_, body = iget(t, ts.URL+"/insights?range=24h")
	if !strings.Contains(body, `aria-current="page">24h<`) {
		t.Error("range picker marks the current range")
	}
	if !strings.Contains(body, "Bash") || !strings.Contains(body, "~/acme/web") && !strings.Contains(body, "/home/dev/acme/web") {
		t.Error("tool and project should be listed")
	}
}

func TestInsightsAPI(t *testing.T) {
	ts, _ := insightsServer(t)
	code, body := iget(t, ts.URL+"/api/insights/llm?range=24h")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var v struct {
		Labels []string `json:"labels"`
		LLM    struct {
			Totals insights.Tokens `json:"totals"`
		} `json:"llm"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Labels) != 24 || v.LLM.Totals.Output != 50 || v.LLM.Totals.CacheRead != 500 {
		t.Fatalf("%+v", v)
	}
	for _, sec := range []string{"kpis", "callouts", "quota", "cost", "sessions", "fleet", "chat"} {
		if code, body := iget(t, ts.URL+"/api/insights/"+sec); code != 200 || !strings.Contains(body, `"`+sec+`"`) {
			t.Errorf("%s: %d %s", sec, code, body)
		}
	}
	if code, _ := iget(t, ts.URL+"/api/insights/nope"); code != 404 {
		t.Errorf("unknown section: %d", code)
	}
	if code, body := iget(t, ts.URL+"/api/insights/sessions/1"); code != 200 || !strings.Contains(body, `"tool_calls":1`) {
		t.Errorf("session stats: %d %s", code, body)
	}
	// Tailnet only.
	req := httptest.NewRequest("GET", "/api/insights", nil)
	req.RemoteAddr = "203.0.113.9:4000"
	rec := httptest.NewRecorder()
	srv := &Server{Store: nil}
	srv.tailnetOnly(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("public peer got %d", rec.Code)
	}
}

func TestLogSplit(t *testing.T) {
	short := strings.Repeat("line\n", 50)
	if p := logSplit(short); p.Head != "" || p.Tail != short {
		t.Fatal("short logs stay whole")
	}
	var b strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "l%d\n", i)
	}
	p := logSplit(b.String())
	if p.Head+p.Tail != b.String() || p.TailLines != logTailLines || !strings.HasPrefix(p.Tail, "l240\n") {
		t.Fatalf("head %d lines, tail %d lines starting %q", p.HeadLines, p.TailLines, p.Tail[:6])
	}
}

func TestGroupTools(t *testing.T) {
	mk := func(kind, tool string, isErr bool) item {
		return item{Message: store.Message{Kind: kind, ToolName: tool, IsError: isErr}}
	}
	items := []item{mk("text", "", false), mk("tool_use", "Bash", false), mk("tool_use", "Bash", false),
		mk("tool_use", "Read", false), mk("tool_result", "", true), mk("text", "", false), mk("tool_use", "Edit", false)}
	items[2].Result = &store.Message{IsError: true}
	g := groupTools(items)
	if len(g) != 4 || g[1].Run == nil || len(g[1].Run) != 4 || g[3].Run != nil {
		t.Fatalf("want text, [4 tools], text, lone tool; got %d groups", len(g))
	}
	if g[1].Summary != "4 tool calls · Bash ×2, Read ×1, result ×1 · 2 failed" {
		t.Fatalf("summary %q", g[1].Summary)
	}
}
