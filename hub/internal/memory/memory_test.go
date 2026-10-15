package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Service{S: st}
}

func w(kind, title, body, repo string) Write {
	return Write{Kind: kind, Title: title, Body: body, Scope: Scope{Repo: repo}, Author: Author{Agent: "claude-code", Host: "studio"}}
}

// ---------------------------------------------------------------- secrets

func TestSecretFilter(t *testing.T) {
	refuse := []string{
		"the deploy key is AKIAABCDEFGHIJKLMNOP",
		"use ghp_abcdefghijklmnopqrstuvwxyz0123456789 for the bot",
		"github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyzABCDEFGH",
		"slack: xoxb-1234567890-abcdefghijkl",
		"export ANTHROPIC_API_KEY=sk-ant-api03-abcdefghijklmnopqrstuvwx",
		"OPENAI sk-proj-abcdefghijklmnopqrstuvwxyz012345",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA",
		"password=hunter2hunter2",
		`{"client_secret": "9f8a7b6c5d4e3f2a1b0c"}`,
		"curl -H 'Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789'",
		"postgres://admin:s3cretPass@db.internal:5432/app",
		"token: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"stray key Xk9pQ2rT7vW3yZ8aB4cD6eF1gH5jK0mN2pQ4sT6vX8z",
		"AIzaSyA1234567890abcdefghijklmnopqrstuv",
		"stripe sk_live_abcdefghijklmnop1234",
	}
	for _, s := range refuse {
		if Secret(s) == "" {
			t.Errorf("not caught: %q", s)
		}
	}
	allow := []string{
		"RDS refuses IAM tokens on replication connections; use the password from Secrets Manager item rds/replica",
		"the token is in 1Password item 'Slack bot' — never paste it",
		"password=$DB_PASSWORD from ~/.config/llm-brain/secrets.env",
		"API_KEY=<redacted>",
		"auth: required for every route under /api",
		"commit 3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a fixed it; sha256 e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"run `make -C hub check` before pushing; internal/web/server.go owns the routes",
		"use the token_type=bearer field",
		"author: claude-code wrote internal_review_comments_handler_and_more_stuff",
		"Co-Authored-By: Claude <noreply@anthropic.com> must never appear in commits",
		"the bot reads `token: `FLAGTGL_TOKEN`` from the env",
		"see internal_auth.rs: 86-120 for the check",
		"set auth0M2m.provider: `keycloak` in the configmap",
		"launch template HttpTokens=optional, then required",
		"workflow uses `secrets: inherit`.",
	}
	for _, s := range allow {
		if f := Secret(s); f != "" {
			t.Errorf("false positive (%s): %q", f, s)
		}
	}
}

// Nothing secret reaches the store, in any field.
func TestNothingSecretIsStored(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t)
	key := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	for _, x := range []Write{
		w(KindFact, "bot token", "use "+key, "acme/api"),
		w(KindFact, "token "+key, "body", "acme/api"),
		{Kind: KindFact, Title: "t", Body: "b", Scope: Scope{Repo: "acme/api", Files: []string{key + ".txt"}}, Author: Author{Host: "h"}},
	} {
		_, err := svc.Save(ctx, x)
		var ie InputError
		if !errors.As(err, &ie) || !strings.Contains(err.Error(), "credential") {
			t.Fatalf("want refusal, got %v", err)
		}
		if strings.Contains(err.Error(), key) {
			t.Fatal("the refusal repeats the secret")
		}
	}
	var n int
	if err := svc.S.DB.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows: %d %v", n, err)
	}
}

func TestValidation(t *testing.T) {
	now := time.Now()
	for name, x := range map[string]Write{
		"kind":       w("musing", "t", "b", "acme/api"),
		"scope":      {Kind: KindFact, Title: "t", Body: "b", Author: Author{Host: "h"}},
		"repo":       w(KindFact, "t", "b", "not a repo"),
		"issue":      {Kind: KindFact, Title: "t", Body: "b", Scope: Scope{Folder: "/x", Issue: 3}, Author: Author{Host: "h"}},
		"big":        w(KindFact, "t", strings.Repeat("transcript ", 300), "acme/api"),
		"author":     {Kind: KindFact, Title: "t", Body: "b", Scope: Scope{Repo: "a/b"}},
		"confidence": {Kind: KindFact, Title: "t", Body: "b", Scope: Scope{Repo: "a/b"}, Author: Author{Host: "h"}, Confidence: "certain"},
		"folder":     {Kind: KindFact, Title: "t", Body: "b", Scope: Scope{Folder: "rel/path"}, Author: Author{Host: "h"}},
	} {
		if _, err := Normalize(x, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	m, err := Normalize(Write{Kind: "Gotcha", Body: "One line\nmore detail", Scope: Scope{Repo: "acme/api.git"}, Author: Author{Host: "h"}}, now)
	if err != nil || m.Title != "One line" || m.Scope.Repo != "acme/api" || m.Kind != KindGotcha || m.Confidence != "medium" {
		t.Fatalf("%+v %v", m, err)
	}
	h, _ := Normalize(w(KindHandoff, "t", "b", "a/b"), now)
	f, _ := Normalize(w(KindFact, "t", "b", "a/b"), now)
	d, _ := Normalize(w(KindDecision, "t", "b", "a/b"), now)
	if h.ExpiresAt.Sub(now) != 14*24*time.Hour || f.ExpiresAt.Sub(now) != 180*24*time.Hour || !d.ExpiresAt.IsZero() {
		t.Fatalf("expiry defaults: %v %v %v", h.ExpiresAt, f.ExpiresAt, d.ExpiresAt)
	}
}

// ---------------------------------------------------------------- dedupe

func TestDedupe(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t)
	first, err := svc.Save(ctx, w(KindGotcha, "RDS refuses IAM tokens on replication", "RDS refuses IAM auth tokens on the replication connection; use the password user.", "acme/api"))
	if err != nil || first.Outcome != "created" {
		t.Fatal(first, err)
	}
	// The same words, different case and spacing: a no-op.
	dup, err := svc.Save(ctx, w(KindGotcha, "rds refuses IAM tokens   on replication", "RDS refuses IAM auth tokens on the replication connection;  use the password user.", "acme/api"))
	if err != nil || dup.Outcome != "duplicate" || dup.Memory.ID != first.Memory.ID {
		t.Fatal(dup, err)
	}
	// Nearly the same: supersedes, doesn't add.
	near, err := svc.Save(ctx, w(KindGotcha, "RDS refuses IAM tokens on replication", "RDS refuses IAM auth tokens on the replication connection; use the password user instead.", "acme/api"))
	if err != nil || near.Outcome != "superseded" || near.Supersedes != first.Memory.ID {
		t.Fatal(near, err)
	}
	// The same words in another repo is another memory.
	other, err := svc.Save(ctx, w(KindGotcha, "RDS refuses IAM tokens on replication", "RDS refuses IAM auth tokens on the replication connection; use the password user instead.", "acme/web"))
	if err != nil || other.Outcome != "created" {
		t.Fatal(other, err)
	}
	// Superseding one that was already superseded is refused.
	x := w(KindFact, "new", "something else entirely", "acme/api")
	x.Supersedes = first.Memory.ID
	if _, err := svc.Save(ctx, x); err == nil || !strings.Contains(err.Error(), "already superseded") {
		t.Fatal(err)
	}
	act, _ := svc.Active(ctx)
	if len(act) != 2 {
		t.Fatalf("active: %d", len(act))
	}
	old, _ := svc.Get(ctx, first.Memory.ID)
	if old.Status != "superseded" || old.Superseded != near.Memory.ID {
		t.Fatalf("old: %+v", old)
	}
	// A fresh service reads the same state back.
	svc2 := &Service{S: svc.S}
	act2, _ := svc2.Active(ctx)
	if len(act2) != 2 {
		t.Fatalf("reloaded: %d", len(act2))
	}
}

func TestExpiredRepeatRenews(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t)
	now := time.Now()
	svc.now = func() time.Time { return now }
	x := w(KindHandoff, "handing off", "tests pass, the migration is left", "acme/api")
	x.Scope.Issue = 7
	a, _ := svc.Save(ctx, x)
	now = now.Add(15 * 24 * time.Hour) // past the 14-day handoff expiry
	b, err := svc.Save(ctx, x)
	if err != nil || b.Outcome != "superseded" || b.Supersedes != a.Memory.ID {
		t.Fatal(b, err)
	}
}

// ---------------------------------------------------------------- context pack

func TestScoreShaping(t *testing.T) {
	now := time.Now()
	mk := func(id int64, kind, title string, sc Scope, age time.Duration) Memory {
		return Memory{ID: id, Kind: kind, Title: title, Body: title, Scope: sc, Status: "active", Confidence: "medium", CreatedAt: now.Add(-age)}
	}
	ms := []Memory{
		mk(1, KindFact, "repo-wide fact", Scope{Repo: "acme/api"}, time.Hour),
		mk(2, KindGotcha, "about this issue", Scope{Repo: "acme/api", Issue: 12}, 48*time.Hour),
		mk(3, KindFact, "about the router", Scope{Repo: "acme/api", Files: []string{"internal/router"}}, time.Hour),
		mk(4, KindFact, "other repo", Scope{Repo: "acme/web"}, time.Hour),
		mk(5, KindHandoff, "handoff for another issue", Scope{Repo: "acme/api", Issue: 99}, time.Hour),
		mk(6, KindHandoff, "handoff for this issue", Scope{Repo: "acme/api", Issue: 12}, time.Hour),
		mk(7, KindFact, "old repo fact", Scope{Repo: "acme/api"}, 400*24*time.Hour),
		mk(9, KindFact, "folder fact", Scope{Folder: "/Users/me/work/api"}, time.Hour),
	}
	exp := mk(8, KindFact, "expired", Scope{Repo: "acme/api"}, time.Hour)
	exp.ExpiresAt = now.Add(-time.Minute)
	ms = append(ms, exp)
	items := Score(ms, Request{Repo: "acme/api", Issue: 12, Folder: "/Users/me/work/api", Files: []string{"internal/router/mux.go"}}, now)
	var ids []int64
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	want := []int64{6, 2, 3, 9, 1, 7}
	if len(ids) != len(want) {
		t.Fatalf("got %v want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v want %v", ids, want)
		}
	}
	// Issue 5's handoff, the other repo and the expired one are left out.
	if items[0].Why != "this issue" {
		t.Fatalf("why: %q", items[0].Why)
	}
}

type fakeSrc struct {
	sessions  []store.Session
	runs      []store.Run
	terminals []store.Terminal
}

func (f fakeSrc) LiveSessions(context.Context) ([]store.Session, error) { return f.sessions, nil }
func (f fakeSrc) RunningRuns(context.Context) ([]store.Run, error)      { return f.runs, nil }
func (f fakeSrc) Terminals(context.Context, string, int) ([]store.Terminal, error) {
	return f.terminals, nil
}

func TestContextPackAndMarkdown(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(t)
	for i, x := range []Write{
		w(KindGotcha, "migrations run twice on deploy", "The `migrate` step runs in both the init container and the app; guard with --once.", "acme/api"),
		w(KindDecision, "keep Turso single connection", "One connection: every write is serialized.", "acme/api"),
		w(KindFact, "web uses pnpm", "The web repo uses pnpm, not npm.", "acme/web"),
	} {
		if _, err := svc.Save(ctx, x); err != nil {
			t.Fatal(i, err)
		}
	}
	src := fakeSrc{runs: []store.Run{{ID: 4, Host: "studio", Provider: "codex", Key: "issue-12", Target: "https://github.com/acme/api/issues/12", HostLive: true, StartedAt: store.Now()}}}
	pk, err := svc.Context(ctx, src, Request{Repo: "acme/api", Issue: 12, K: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(pk.Memories) != 1 || pk.Memories[0].Kind != KindGotcha || !pk.Memories[0].Verify || pk.Remote != "off" {
		t.Fatalf("%+v", pk)
	}
	if len(pk.Presence) != 1 || pk.Presence[0].Why != "same issue" {
		t.Fatalf("presence: %+v", pk.Presence)
	}
	md := Markdown(pk, Request{})
	for _, s := range []string{"## Shared memory", "migrations run twice", "verify before use", "codex on studio (same issue"} {
		if !strings.Contains(md, s) {
			t.Errorf("markdown lacks %q:\n%s", s, md)
		}
	}
	if strings.Contains(md, "pnpm") {
		t.Error("another repo's memory leaked in")
	}
	empty, _ := svc.Context(ctx, src, Request{Repo: "nobody/here"})
	if Markdown(empty, Request{}) != "" {
		t.Error("an empty pack should render nothing")
	}
}

// ---------------------------------------------------------------- presence

func TestPresence(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	src := fakeSrc{
		sessions: []store.Session{
			{ID: 1, Host: "studio", Provider: "claude", Cwd: "/Users/me/work/api-wt-fix-login", Branch: "17446-fix-login", FileMtime: store.T(now)},
			{ID: 2, Host: "laptop", Provider: "codex", Cwd: "/Users/me/work/api", Branch: "main", FileMtime: store.T(now.Add(-time.Minute))},
			{ID: 3, Host: "laptop", Provider: "claude", Cwd: "/Users/me/work/web", FileMtime: store.T(now), UUID: "self-uuid"},
			{ID: 4, Host: "studio", Provider: "claude", Cwd: "/Users/me/work/api", RunID: 9, FileMtime: store.T(now)}, // a run's own transcript
		},
		runs: []store.Run{
			{ID: 9, Host: "studio", Provider: "claude", Key: "pr-api-55", Target: "https://github.com/acme/api/pull/55", HostLive: true, StartedAt: store.T(now)},
			{ID: 10, Host: "gone", Provider: "claude", Key: "issue-1", Target: "https://github.com/acme/api/issues/17446", HostLive: false},
		},
		terminals: []store.Terminal{
			{ID: 5, Host: "studio", Provider: "gemini", Cwd: "/Users/me/work/api/internal", Status: "live", UpdatedAt: store.T(now)},
			{ID: 6, Host: "studio", Provider: "claude", Cwd: "/Users/me/work/api", Status: "ended"},
		},
	}
	ps, err := Who(ctx, src, Want{Repo: "acme/api", Issue: 17446, Folder: "/Users/me/work/api"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range ps {
		got[p.Link] = p.Why
	}
	want := map[string]string{"/sessions/1": "same issue", "/sessions/2": "same folder", "/terminals/5": "same folder", "/runs/9": "same repo"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q want %q (all %v)", k, got[k], v, got)
		}
	}
	if ps[0].Link != "/sessions/1" {
		t.Fatalf("same issue first: %+v", ps[0])
	}
	all, _ := Who(ctx, src, Want{Self: "self-uuid"})
	if len(all) != 4 { // 1, 2, run 9, terminal 5: not the asker, not the run's transcript, not ended or dead-host ones
		t.Fatalf("all: %d %+v", len(all), all)
	}
}

// ---------------------------------------------------------------- TaskAI outbox

type fakeTaskAI struct {
	mu        sync.Mutex
	failFirst int // answer 500 to the first n page creations
	calls     int
	unauth    bool
	pages     map[int64]string
	titles    map[int64]string
	projects  []project
	searchFor func(q string) []Hit
	delay     time.Duration
	authSeen  string
}

func (f *fakeTaskAI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.authSeen = r.Header.Get("Authorization")
		if f.unauth {
			http.Error(w, `{"error":"invalid"}`, http.StatusUnauthorized)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/projects":
			_ = json.NewEncoder(w).Encode(f.projects)
		case r.Method == "POST" && r.URL.Path == "/api/projects":
			p := project{ID: 77, Name: in["name"].(string)}
			f.projects = append(f.projects, p)
			_ = json.NewEncoder(w).Encode(p)
		case r.Method == "POST" && r.URL.Path == "/api/projects/77/wiki/pages":
			f.calls++
			if f.calls <= f.failFirst {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			id := int64(100 + len(f.pages))
			f.pages[id] = ""
			f.titles[id] = in["title"].(string)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
		case r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/content"):
			var id int64
			_, _ = fmtSscanf(r.URL.Path, &id)
			f.pages[id] = in["content"].(string)
		case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/api/wiki/pages/"):
			var id int64
			_, _ = fmtSscanf(r.URL.Path, &id)
			f.titles[id] = in["title"].(string)
		case r.Method == "POST" && r.URL.Path == "/api/wiki/search":
			if f.delay > 0 {
				f.mu.Unlock()
				time.Sleep(f.delay)
				f.mu.Lock()
			}
			var hits []Hit
			if f.searchFor != nil {
				hits = f.searchFor(in["query"].(string))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": hits})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

func fmtSscanf(p string, id *int64) (int, error) {
	p = strings.TrimPrefix(p, "/api/wiki/pages/")
	p, _, _ = strings.Cut(p, "/")
	return 1, json.Unmarshal([]byte(p), id)
}

func TestOutboxRetries(t *testing.T) {
	ctx := context.Background()
	f := &fakeTaskAI{failFirst: 2, pages: map[int64]string{}, titles: map[int64]string{}}
	ts := httptest.NewServer(f.handler(t))
	defer ts.Close()
	svc := newSvc(t)
	now := time.Now().Truncate(time.Millisecond) // the store keeps milliseconds
	svc.now = func() time.Time { return now }
	svc.TaskAI = &TaskAI{URL: ts.URL, Key: "k-test"}
	res, err := svc.Save(ctx, w(KindGotcha, "flaky deploys", "The deploy step flakes when the cache is cold.", "acme/api"))
	if err != nil || res.Memory.SyncState != "pending" {
		t.Fatal(res, err)
	}
	// First try fails: backoff 30 s.
	if n, err := svc.SyncOnce(ctx); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	m, _ := svc.Get(ctx, res.Memory.ID)
	if m.Attempts != 1 || m.LastError == "" || m.NextTry.Sub(now) != 30*time.Second {
		t.Fatalf("after 1: %+v", m)
	}
	// Not due yet: nothing is sent.
	if n, _ := svc.SyncOnce(ctx); n != 0 || f.calls != 1 {
		t.Fatalf("sent before due: %d calls", f.calls)
	}
	now = now.Add(31 * time.Second)
	svc.SyncOnce(ctx)
	m, _ = svc.Get(ctx, res.Memory.ID)
	if m.Attempts != 2 || m.NextTry.Sub(now) != time.Minute {
		t.Fatalf("after 2: %+v", m)
	}
	now = now.Add(2 * time.Minute)
	if n, err := svc.SyncOnce(ctx); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	m, _ = svc.Get(ctx, res.Memory.ID)
	if m.SyncState != "synced" || m.PageID == 0 || m.LastError != "" {
		t.Fatalf("synced: %+v", m)
	}
	if f.authSeen != "ApiKey k-test" {
		t.Fatalf("auth header %q", f.authSeen)
	}
	if !strings.Contains(f.pages[m.PageID], "The deploy step flakes") || !strings.Contains(f.titles[m.PageID], "[gotcha] acme/api: flaky deploys") {
		t.Fatalf("page: %q %q", f.titles[m.PageID], f.pages[m.PageID])
	}
	if len(f.projects) != 1 || f.projects[0].Name != "Agent memory" {
		t.Fatalf("project: %+v", f.projects)
	}
	// Superseding it rewrites the old page as superseded and makes a new one.
	nr, _ := svc.Save(ctx, w(KindGotcha, "flaky deploys", "The deploy step flakes when the cache is cold; warm it first.", "acme/api"))
	if nr.Outcome != "superseded" {
		t.Fatal(nr)
	}
	if n, _ := svc.SyncOnce(ctx); n != 2 {
		t.Fatalf("sent %d", n)
	}
	if !strings.HasPrefix(f.titles[m.PageID], "[superseded]") || !strings.Contains(f.pages[m.PageID], "Superseded by memory") {
		t.Fatalf("old page: %q", f.titles[m.PageID])
	}
	// A bad key stops the batch and is retried later.
	f.unauth = true
	svc.Save(ctx, w(KindFact, "one", "first fact here", "acme/one"))
	svc.Save(ctx, w(KindFact, "two", "second fact here", "acme/two"))
	if _, err := svc.SyncOnce(ctx); err == nil {
		t.Fatal("want an error for 401")
	}
	c, _ := svc.Counts(ctx)
	if c.Pending != 2 || c.Failing != 1 {
		t.Fatalf("counts: %+v", c)
	}
}

func TestRemoteSearchBudgetAndCache(t *testing.T) {
	ctx := context.Background()
	f := &fakeTaskAI{pages: map[int64]string{}, titles: map[int64]string{}, projects: []project{{ID: 77, Name: "agent memory"}}}
	ts := httptest.NewServer(f.handler(t))
	defer ts.Close()
	svc := newSvc(t)
	svc.TaskAI = &TaskAI{URL: ts.URL, Key: "k"}
	svc.RemoteBudget = 50 * time.Millisecond
	res, _ := svc.Save(ctx, w(KindFact, "local", "a local fact", "acme/api"))
	setSynced(ctx, svc.S.DB, res.Memory.ID, 500)
	svc.Load(ctx)
	f.searchFor = func(q string) []Hit {
		if !strings.Contains(q, "acme/api") {
			t.Errorf("query %q", q)
		}
		return []Hit{{PageID: 500, PageTitle: "[fact] acme/api: local"}, {PageID: 9, PageTitle: "Runbook: deploys", Snippet: "how we deploy"},
			{PageID: 10, PageTitle: "[superseded] [fact] x"}}
	}
	f.delay = 300 * time.Millisecond
	start := time.Now()
	pk, err := svc.Context(ctx, nil, Request{Repo: "acme/api"})
	if err != nil || pk.Remote != "timeout" || len(pk.Memories) != 1 || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("%+v %v %v", pk, err, time.Since(start))
	}
	time.Sleep(400 * time.Millisecond) // the search finishes in the background and fills the cache
	pk, _ = svc.Context(ctx, nil, Request{Repo: "acme/api"})
	if pk.Remote != "cached" || len(pk.Wiki) != 1 || pk.Wiki[0].PageID != 9 || !strings.Contains(pk.Memories[0].Why, "search") {
		t.Fatalf("%+v", pk)
	}
}

// ---------------------------------------------------------------- import

func TestImportClaude(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// A working dir with a dash in its name, to decode: <root>/projects/llm-brain
	work := filepath.Join(root, "projects", "llm-brain")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	enc := strings.ReplaceAll(work, "/", "-")
	if got := DecodeProjectDir(enc); got != work {
		t.Fatalf("decode %s: %s", enc, got)
	}
	dir := filepath.Join(root, "claude-projects")
	mdir := filepath.Join(dir, enc, "memory")
	if err := os.MkdirAll(mdir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(mdir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("MEMORY.md", "- [Hub](hub.md) — index\n")
	write("hub.md", "---\nname: hub-workflow\ndescription: Run make check before committing\nmetadata:\n  type: feedback\n---\n\nRun `make -C hub check` before committing.\n")
	write("leak.md", "---\nname: leak\ndescription: a key\ntype: reference\n---\nkey AKIAABCDEFGHIJKLMNOP\n")
	write("bad.md", "no frontmatter")
	svc := newSvc(t)
	post := func(ctx context.Context, x Write) (Result, error) { return svc.Save(ctx, x) }
	st, err := ImportClaude(ctx, dir, "studio", post)
	if err != nil || st.Files != 3 || st.Created != 1 || st.Refused != 2 {
		t.Fatalf("%+v %v", st, err)
	}
	act, _ := svc.Active(ctx)
	m := act[0]
	if m.Kind != KindDecision || m.Title != "Run make check before committing" || m.Scope.Folder != work || m.Source != "import" || !m.ExpiresAt.IsZero() {
		t.Fatalf("%+v", m)
	}
	// Again: nothing changes.
	st, _ = ImportClaude(ctx, dir, "studio", post)
	if st.Duplicate != 1 || st.Created != 0 {
		t.Fatalf("second run: %+v", st)
	}
	// Edited: supersedes its earlier import.
	write("hub.md", "---\nname: hub-workflow\ndescription: Run make check before committing\ntype: feedback\n---\nAlways run `make -C hub check` and the UI smoke test before committing.\n")
	st, _ = ImportClaude(ctx, dir, "studio", post)
	if st.Superseded != 1 {
		t.Fatalf("edit: %+v", st)
	}
	if act, _ := svc.Active(ctx); len(act) != 1 {
		t.Fatalf("active after edit: %d", len(act))
	}
	// A second, similar file is its own memory, and stays so on every run.
	write("hub2.md", "---\nname: hub-workflow-2\ndescription: Run make check before committing, always\ntype: feedback\n---\nAlways run `make -C hub check` and the UI smoke test before committing!\n")
	for i := 0; i < 2; i++ {
		ImportClaude(ctx, dir, "studio", post)
		if act, _ := svc.Active(ctx); len(act) != 2 {
			t.Fatalf("run %d: two similar files: %d active", i, len(act))
		}
	}
	// An agent writes a better version of an imported memory: the next import leaves it alone.
	act, _ = svc.Active(ctx)
	better := Write{Kind: KindDecision, Title: "Run make check before committing", Body: "Always run `make -C hub check` and the UI smoke test before committing; CI runs the same.",
		Scope: Scope{Folder: work}, Author: Author{Agent: "codex", Host: "laptop"}}
	better.Supersedes = act[0].ID
	if _, err := svc.Save(ctx, better); err != nil {
		t.Fatal(err)
	}
	st, _ = ImportClaude(ctx, dir, "studio", post)
	if st.Created != 0 || st.Superseded != 0 || st.Duplicate != 2 {
		t.Fatalf("after an agent's rewrite: %+v", st)
	}
}
