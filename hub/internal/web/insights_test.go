package web

import (
	"context"
	"encoding/json"
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
	ts, _ := insightsServer(t)
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
	_, body := iget(t, ts.URL+"/insights?range=24h")
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
