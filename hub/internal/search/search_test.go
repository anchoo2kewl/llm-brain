package search

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/ingest"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func terms(s string) []string {
	var out []string
	for _, t := range (codeTokenizer{}).Tokenize([]byte(s)) {
		out = append(out, fmt.Sprintf("%s@%d", t.Term, t.Position))
	}
	return out
}

func TestAnalyzerCodeTokens(t *testing.T) {
	got := terms(`See uiv3#835 in "blue-stack-api/internal/x.go", tenant_id=3F2A6C1E-0B4D-4E5F-8A9B-0C1D2E3F4A5B; Deploying (done).`)
	want := []string{
		"see@1",
		"uiv3#835@2", "uiv3@2", "835@3",
		"in@4",
		"blue-stack-api/internal/x.go@5", "blue@5", "stack@6", "api@7", "internal@8", "x@9", "go@10",
		"tenant_id@11", "tenant@11", "id@12",
		"3f2a6c1e-0b4d-4e5f-8a9b-0c1d2e3f4a5b@13", "3f2a6c1e@13", "0b4d@14", "4e5f@15", "8a9b@16", "0c1d2e3f4a5b@17",
		"deploying@18", // no stemming
		"done@19",      // "(done)." is just its word
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tokens\n got %v\nwant %v", got, want)
	}
	// Blobs aren't indexed; their neighbours keep their positions.
	blob := strings.Repeat("A", 100)
	if got := terms("x " + blob + " y"); !slices.Equal(got, []string{"x@1", "y@2"}) {
		t.Fatalf("blob: %v", got)
	}
	a := AnalyzeWord("Blue-Stack-API")
	if a.Whole != "blue-stack-api" || !slices.Equal(a.Parts, []string{"blue", "stack", "api"}) {
		t.Fatalf("AnalyzeWord: %+v", a)
	}
	if a := AnalyzeWord("deploy"); a.Whole != "" || !slices.Equal(a.Parts, []string{"deploy"}) {
		t.Fatalf("AnalyzeWord plain: %+v", a)
	}
	if p := AnalyzePhrase("fix blue-stack now"); fmt.Sprint(p) != "[[fix] [blue-stack blue] [stack] [now]]" {
		t.Fatalf("AnalyzePhrase: %v", p)
	}
}

func TestParse(t *testing.T) {
	loc := time.UTC
	p := Parse(`deploy "kubectl get pods" roll* -flaky host:Studio type:runs provider:codex kind:tool_use tool:Bash `+
		`session:42 after:2026-10-01 before:2026-10-14 https://x.y/z uiv3#835 host:"lap top" status:failed bogus:thing`, loc)
	var must []string
	for _, c := range p.Must {
		must = append(must, fmt.Sprintf("%d:%s", c.Kind, c.Text))
	}
	wantMust := []string{"0:deploy", "1:kubectl get pods", "2:roll", "0:https://x.y/z", "0:uiv3#835", "0:bogus:thing"}
	if !slices.Equal(must, wantMust) {
		t.Fatalf("must: %v", must)
	}
	if len(p.Not) != 1 || p.Not[0].Text != "flaky" {
		t.Fatalf("not: %+v", p.Not)
	}
	wantF := map[string][]string{"host": {"Studio", "lap top"}, "type": {"run"}, "provider": {"codex"}, "kind": {"tool_use"},
		"tool": {"bash"}, "session": {"42"}, "status": {"failed"}}
	if fmt.Sprint(p.Filters) != fmt.Sprint(wantF) {
		t.Fatalf("filters: %v", p.Filters)
	}
	if !p.After.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, loc)) || !p.Before.Equal(time.Date(2026, 10, 14, 0, 0, 0, 0, loc)) {
		t.Fatalf("dates: %v %v", p.After, p.Before)
	}
	if p := Parse("type:run", loc); p.HasText() || p.Empty() {
		t.Fatalf("filter-only query: %+v", p)
	}
	if p := Parse("  ", loc); !p.Empty() {
		t.Fatal("blank query not empty")
	}
	if p := Parse("after:notadate x*", loc); !p.After.IsZero() || len(p.Must) != 2 || p.Must[1].Kind != Word {
		t.Fatalf("bad date / one-letter prefix: %+v", p)
	}
}

func TestSnippet(t *testing.T) {
	text := strings.Repeat("filler ", 100) + "we merged <uiv3#835> after the Deploy finished" + strings.Repeat(" tail", 100)
	s := string(Snippet(text, Parse("deploy uiv3#835", nil).HighlightTerms()))
	if !strings.Contains(s, "<mark>uiv3#835</mark>") || !strings.Contains(s, "<mark>Deploy</mark>") {
		t.Fatalf("snippet: %s", s)
	}
	if !strings.Contains(s, "&lt;") || strings.Contains(s, "<uiv3") {
		t.Fatalf("snippet not escaped: %s", s)
	}
	if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") {
		t.Fatalf("snippet not windowed: %s", s)
	}
	// Prefix terms run to the end of the word; plain terms only match whole words.
	s = string(Snippet("redeploy deployment deploy", Parse("deploy*", nil).HighlightTerms()))
	if s != "redeploy <mark>deployment</mark> <mark>deploy</mark>" {
		t.Fatalf("prefix snippet: %s", s)
	}
}

// ---------------------------------------------------------------- against a real store

type fixture struct {
	st  *store.Store
	eng *ingest.Engine
	idx *Index
	ctx context.Context
}

func newFixture(t *testing.T, indexPath string) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{st: st, eng: ingest.NewEngine(st, "elastio", "elastio/blue-stack"), ctx: context.Background()}
	f.open(t, indexPath)
	return f
}

func (f *fixture) open(t *testing.T, path string) {
	t.Helper()
	idx, err := Open(path, f.st, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	idx.Loc = time.UTC
	f.idx = idx
	t.Cleanup(func() { idx.Close() })
	f.eng.OnWrite = func(ctx context.Context) {
		if err := idx.Sync(ctx); err != nil {
			t.Errorf("sync: %v", err)
		}
	}
}

func (f *fixture) chunk(t *testing.T, host, kind, path string, data string) {
	t.Helper()
	var off int64
	_ = f.st.DB.QueryRow(`SELECT off FROM files WHERE host = ? AND path = ?`, host, path).Scan(&off)
	if _, err := f.eng.Chunk(f.ctx, ingest.Chunk{Host: host, Kind: kind, Path: path, Offset: off,
		Mtime: time.Now().Add(-time.Hour), Data: []byte(data)}); err != nil {
		t.Fatal(err)
	}
}

func user(sess, ts, text string) string {
	return fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":"/w/blue-stack","gitBranch":"feat/x","timestamp":%q,"message":{"role":"user","content":%q}}`+"\n", sess, ts, text)
}

func toolUse(sess, ts, id, cmd string) string {
	return fmt.Sprintf(`{"type":"assistant","sessionId":%q,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"Bash","input":{"command":%q}}]}}`+"\n", sess, ts, id, cmd)
}

func toolResult(sess, ts, id, out string) string {
	return fmt.Sprintf(`{"type":"user","sessionId":%q,"timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`+"\n", sess, ts, id, out)
}

const (
	uuidA = "3f2a6c1e-0b4d-4e5f-8a9b-0c1d2e3f4a5b"
	uuidB = "99999999-0b4d-4e5f-8a9b-0c1d2e3f4a5b"
)

func (f *fixture) seed(t *testing.T) {
	f.chunk(t, "studio", ingest.KindClaude, "/p/-w/"+uuidA+".jsonl",
		user(uuidA, "2026-10-14T20:00:01Z", "please deploy blue-stack-api and check uiv3#835")+
			toolUse(uuidA, "2026-10-14T20:00:02Z", "t1", "cd ~/w\nkubectl get pods -n blues")+
			toolResult(uuidA, "2026-10-14T20:00:03Z", "t1", "tenant_id missing in internal/web/server.go"))
	f.chunk(t, "laptop", ingest.KindCodex, "/c/2026/09/01/rollout-2026-09-01T10-00-00-"+uuidB+".jsonl",
		`{"timestamp":"2026-09-01T10:00:00Z","type":"session_meta","payload":{"id":"`+uuidB+`","cwd":"/w/other","originator":"codex_cli_rs"}}`+"\n"+
			`{"timestamp":"2026-09-01T10:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"redeploy the deployment of other-thing"}]}}`+"\n")
	f.chunk(t, "studio", ingest.KindRunLog, "/s/logs/review-blue-stack-uiv3-839.20261014-160000.log",
		"working on it\nREVIEWED https://github.com/elastio/blue-stack-uiv3/pull/839 APPROVE looks fine\n")
	f.chunk(t, "studio", ingest.KindFleetLog, "/s/logs/fleet.log",
		"2026-10-14 16:00:00 [studio] started review-blue-stack-uiv3-839 (claude/read, pid 1, log /s/logs/review-blue-stack-uiv3-839.20261014-160000.log)\n")
}

func (f *fixture) search(t *testing.T, q string) []string {
	t.Helper()
	return f.searchReq(t, Request{Q: q, Size: 100})
}

func (f *fixture) searchReq(t *testing.T, r Request) []string {
	t.Helper()
	res, err := f.idx.Search(f.ctx, r)
	if err != nil {
		t.Fatalf("%q: %v", r.Q, err)
	}
	var out []string
	for _, h := range res.Hits {
		out = append(out, fmt.Sprintf("%s%d", h.Type[:1], h.ID))
	}
	slices.Sort(out)
	return out
}

func TestSearch(t *testing.T) {
	f := newFixture(t, "")
	f.seed(t)
	if !f.idx.Ready() {
		if err := f.idx.Sync(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Message ids: 1 user prompt, 2 tool_use, 3 tool_result (session 1); 4 codex prompt (session 2).
	cases := map[string][]string{
		"deploy":                    {"m1", "s1"}, // the session's title is its first prompt
		"deploy blue-stack-api":     {"m1", "s1"},
		"deploy nothere":            nil,
		`"kubectl get pods"`:        {"m2"},
		`"get kubectl"`:             nil,
		"deploy*":                   {"m1", "m4", "s1", "s2"},
		"uiv3#835":                  {"m1", "s1"},
		"blue-stack":                {"e1", "m1", "r1", "s1"}, // a run of parts inside longer tokens
		"internal/web/server.go":    {"m3"},
		"web/server":                {"m3"},
		"tenant_id":                 {"m3"},
		uuidA:                       {"s1"},
		"kubectl -pods":             nil,
		"deploy -uiv3#835":          nil,
		"tool:bash":                 {"m2"},
		"kubectl tool:bash":         {"m2"},
		"type:session":              {"s1", "s2"},
		"type:run":                  {"r1"},
		"type:event":                {"e1"},
		"host:laptop":               {"m4", "s2"},
		"provider:codex type:msg":   {"m4"},
		"kind:tool_result":          {"m3"},
		"deploy* before:2026-10-01": {"m4", "s2"},
		"deploy* after:2026-10-01":  {"m1", "s1"},
		"session:1":                 {"m1", "m2", "m3", "s1"},
		"session:3f2a6c1e":          {"m1", "m2", "m3", "s1"},
		"approve":                   {"r1"},
		"pull/839 type:run":         {"r1"},
		"started review*":           {"e1"},
		"status:succeeded":          nil, // still running: no heartbeat closed it
	}
	for q, want := range cases {
		if got := f.search(t, q); !slices.Equal(got, want) {
			t.Errorf("%q: got %v want %v", q, got, want)
		}
	}
	// Menus are filters too.
	if got := f.searchReq(t, Request{Q: "deploy*", Host: "Studio", Type: "messages"}); !slices.Equal(got, []string{"m1"}) {
		t.Errorf("menus: %v", got)
	}
	// Newest first, and paging.
	res, err := f.idx.Search(f.ctx, Request{Q: "session:1", Newest: true, Size: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 4 || len(res.Hits) != 2 || res.Hits[0].Time < res.Hits[1].Time {
		t.Fatalf("newest page 1: total %d %+v", res.Total, res.Hits)
	}
	res2, _ := f.idx.Search(f.ctx, Request{Q: "session:1", Newest: true, Size: 2, Page: 2})
	if len(res2.Hits) != 2 || res2.Hits[0].Time > res.Hits[1].Time {
		t.Fatalf("newest page 2: %+v", res2.Hits)
	}
	// A message links to its place in the transcript.
	res, _ = f.idx.Search(f.ctx, Request{Q: "tenant_id"})
	if h := res.Hits[0]; h.Link != "/sessions/1?after=2#m3" || !strings.Contains(string(h.Snippet), "<mark>tenant_id</mark>") {
		t.Fatalf("hit: %+v", h)
	}
	// An event that names a worker log links to its run.
	res, _ = f.idx.Search(f.ctx, Request{Q: "type:event"})
	if h := res.Hits[0]; h.Link != "/log/review-blue-stack-uiv3-839.20261014-160000.log?host=studio" {
		t.Fatalf("event link: %+v", h)
	}
}

func dirty(t *testing.T, st *store.Store) int {
	var n int
	if err := st.DB.QueryRow(`SELECT count(*) FROM search_dirty`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIncrementalAndIdempotent(t *testing.T) {
	f := newFixture(t, filepath.Join(t.TempDir(), "hub.bleve"))
	if err := f.idx.Sync(f.ctx); err != nil { // empty store: built at once
		t.Fatal(err)
	}
	if !f.idx.Ready() {
		t.Fatal("not ready after building an empty store")
	}
	f.seed(t) // every chunk is indexed in the pass that ingested it
	n := f.idx.DocCount()
	if n != 8 { // messages: prompt, tool_use, tool_result, codex prompt; 2 sessions; 1 run; 1 event
		t.Fatalf("doc count %d", n)
	}
	if d := dirty(t, f.st); d != 0 {
		t.Fatalf("%d dirty rows left", d)
	}
	wm := f.idx.Watermark("messages")
	// Syncing again, and re-sending a chunk the store already has, index nothing new.
	if err := f.idx.Sync(f.ctx); err != nil {
		t.Fatal(err)
	}
	if f.idx.DocCount() != n || f.idx.Watermark("messages") != wm {
		t.Fatalf("second sync changed the index: %d→%d docs, wm %d→%d", n, f.idx.DocCount(), wm, f.idx.Watermark("messages"))
	}
	// New rows and changed rows are picked up.
	f.chunk(t, "studio", ingest.KindClaude, "/p/-w/"+uuidA+".jsonl", user(uuidA, "2026-10-14T20:01:00Z", "now rollback the canary"))
	f.chunk(t, "studio", ingest.KindRunLog, "/s/logs/review-blue-stack-uiv3-839.20261014-160000.log", "posted a nitpick about zanzibar\n")
	if got := f.search(t, "canary"); !slices.Equal(got, []string{"m5"}) {
		t.Fatalf("new message: %v", got)
	}
	if got := f.search(t, "zanzibar"); !slices.Equal(got, []string{"r1"}) {
		t.Fatalf("appended run log: %v", got)
	}
	if f.idx.DocCount() != n+1 {
		t.Fatalf("doc count %d, want %d (a changed run is replaced, not added)", f.idx.DocCount(), n+1)
	}
	// A heartbeat that closes the run re-indexes it with its new status.
	if err := f.eng.Heartbeat(f.ctx, ingest.Heartbeat{Host: "studio", Role: "serve"}); err != nil {
		t.Fatal(err)
	}
	if got := f.search(t, "status:succeeded"); !slices.Equal(got, []string{"r1"}) {
		t.Fatalf("closed run: %v", got)
	}
	if d := dirty(t, f.st); d != 0 {
		t.Fatalf("%d dirty rows left", d)
	}
}

func TestBackfillResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.bleve")
	f := newFixture(t, path)
	f.eng.OnWrite = nil // rows land in the store before the index exists, as on a hub that's upgraded
	var data strings.Builder
	for i := range 57 {
		data.WriteString(user(uuidA, fmt.Sprintf("2026-10-14T20:%02d:00Z", i), fmt.Sprintf("message number %d about walrus", i)))
	}
	f.chunk(t, "studio", ingest.KindClaude, "/p/-w/"+uuidA+".jsonl", data.String())
	if f.idx.Ready() {
		t.Fatal("ready before building")
	}
	// Build part of it, then "crash".
	f.idx.PageSize = 10
	f.idx.mu.Lock()
	for _, tb := range []string{"sessions", "messages", "messages"} {
		if _, err := f.idx.indexPage(f.ctx, tb); err != nil {
			t.Fatal(err)
		}
	}
	f.idx.mu.Unlock()
	if err := f.idx.Close(); err != nil {
		t.Fatal(err)
	}
	f.open(t, path)
	f.idx.PageSize = 10
	if f.idx.Ready() || f.idx.Watermark("messages") != 20 {
		t.Fatalf("after reopen: ready %v wm %d", f.idx.Ready(), f.idx.Watermark("messages"))
	}
	if err := f.idx.Sync(f.ctx); err != nil {
		t.Fatal(err)
	}
	if !f.idx.Ready() || f.idx.Progress() != 100 || f.idx.Watermark("messages") != 57 {
		t.Fatalf("after resume: ready %v progress %d wm %d", f.idx.Ready(), f.idx.Progress(), f.idx.Watermark("messages"))
	}
	if n := f.idx.DocCount(); n != 58 { // 57 messages + 1 session, each once
		t.Fatalf("doc count %d", n)
	}
	res, err := f.idx.Search(f.ctx, Request{Q: "walrus type:message"})
	if err != nil || res.Total != 57 {
		t.Fatalf("walrus: %v %v", res, err)
	}
	// Reopening a built index keeps it ready.
	if err := f.idx.Close(); err != nil {
		t.Fatal(err)
	}
	f.open(t, path)
	if !f.idx.Ready() {
		t.Fatal("built index not ready after reopen")
	}
}

func TestStaleHitsDropped(t *testing.T) {
	f := newFixture(t, "")
	f.seed(t)
	if _, err := f.st.DB.Exec(`DELETE FROM messages WHERE id = 3`); err != nil {
		t.Fatal(err)
	}
	if got := f.search(t, "tenant_id"); len(got) != 0 {
		t.Fatalf("deleted row still a hit: %v", got)
	}
}
