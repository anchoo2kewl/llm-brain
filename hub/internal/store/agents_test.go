package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestCommandQueue(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	id, cid, err := st.CreateAgentSession(ctx, AgentSession{Host: "studio", Cwd: "/w/app", Provider: "claude", Perm: "acceptEdits"},
		&Command{Host: "studio", Kind: "turn", Payload: json.RawMessage(`{"prompt":"hi"}`)})
	if err != nil || id == 0 || cid == 0 {
		t.Fatalf("create: %d %d %v", id, cid, err)
	}
	a, err := st.AgentSession(ctx, id)
	if err != nil || a.Status != "queued" || a.Turns != 1 {
		t.Fatalf("session: %+v %v", a, err)
	}
	// A second turn can't be queued while the first is pending.
	if _, err := st.QueueTurn(ctx, Command{Host: "studio", Kind: "turn", AgentID: id}); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	// Other hosts see nothing; the right host claims it once.
	if cs, _ := st.ClaimCommands(ctx, "laptop"); len(cs) != 0 {
		t.Fatalf("laptop got %v", cs)
	}
	cs, err := st.ClaimCommands(ctx, "studio")
	if err != nil || len(cs) != 1 || cs[0].ID != cid || cs[0].AgentID != id || string(cs[0].Payload) != `{"prompt":"hi"}` {
		t.Fatalf("claim: %+v %v", cs, err)
	}
	if again, _ := st.ClaimCommands(ctx, "studio"); len(again) != 0 {
		t.Fatalf("claimed twice: %v", again)
	}
	if fl, _ := st.InFlight(ctx, "studio"); len(fl) != 1 {
		t.Fatalf("in flight: %v", fl)
	}
	if err := st.FinishCommand(ctx, cid, "done", `{"status":"done"}`); err != nil {
		t.Fatal(err)
	}
	c, _ := st.Command(ctx, cid)
	if c.Status != "done" || c.FinishedAt == "" {
		t.Fatalf("finish: %+v", c)
	}
	idle := "idle"
	_ = st.UpdateAgentSession(ctx, id, AgentUpdate{Status: idle, ProviderSession: "uuid-1"})
	if _, err := st.QueueTurn(ctx, Command{Host: "studio", Kind: "turn", AgentID: id}); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CancelQueued(ctx, id); n != 1 {
		t.Fatalf("cancel: %d", n)
	}
	got, err := st.AgentSessionByProvider(ctx, "studio", "claude", "uuid-1")
	if err != nil || got.ID != id {
		t.Fatalf("by provider: %+v %v", got, err)
	}
}

func TestPromptsAndHosts(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	id, err := st.SavePrompt(ctx, SavedPrompt{Name: "fix", Body: "Fix {{ISSUE}}"})
	if err != nil || id == 0 {
		t.Fatal(id, err)
	}
	if _, err := st.SavePrompt(ctx, SavedPrompt{ID: id, Name: "fix", Body: "Fix {{ISSUE}} now"}); err != nil {
		t.Fatal(err)
	}
	p, _ := st.SavedPrompt(ctx, id)
	if p.Body != "Fix {{ISSUE}} now" {
		t.Fatal(p)
	}
	_ = st.DeletePrompt(ctx, id)
	if ps, _ := st.SavedPrompts(ctx); len(ps) != 0 {
		t.Fatal(ps)
	}
	if err := st.SetAgentInventory(ctx, "laptop", "v1", []string{"/w"}, []AgentFolder{{Path: "/w/app", Branch: "main"}}, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	h, err := st.AgentHost(ctx, "laptop")
	if err != nil || !h.Live() || len(h.Folders) != 1 || len(h.Providers) != 1 || h.Roots[0] != "/w" {
		t.Fatalf("%+v %v", h, err)
	}
}

func TestReportsAreIdempotentAndScopedToTheCurrentTurn(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	id, c1, err := st.CreateAgentSession(ctx, AgentSession{Host: "studio", Cwd: "/w", Provider: "claude", Perm: "acceptEdits"},
		&Command{Host: "studio", Kind: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ApplyReport(ctx, c1, "done", `{}`, AgentUpdate{Status: "idle", ProviderSession: "uuid-1"}); err != nil || !ok {
		t.Fatalf("first report: %v %v", ok, err)
	}
	c2, err := st.QueueTurn(ctx, Command{Host: "studio", Kind: "turn", AgentID: id})
	if err != nil {
		t.Fatal(err)
	}
	// The first turn's final report arrives again (a retry after a lost response): nothing changes.
	for _, status := range []string{"done", "failed", "running"} {
		if _, ok, err := st.ApplyReport(ctx, c1, status, `{}`, AgentUpdate{Status: "idle"}); err != nil || ok {
			t.Fatalf("late %s report applied: %v %v", status, ok, err)
		}
	}
	if a, _ := st.AgentSession(ctx, id); a.Status != "queued" || a.CurrentCmd != c2 {
		t.Fatalf("session moved by an old report: %+v", a)
	}
	if c, _ := st.Command(ctx, c1); c.Status != "done" {
		t.Fatalf("finished command rewritten: %+v", c)
	}
	// A command that isn't the session's current turn records its report but can't move the session.
	x, _ := st.QueueCommand(ctx, Command{Host: "studio", Kind: "turn", AgentID: id})
	if _, ok, _ := st.ApplyReport(ctx, x, "failed", `{}`, AgentUpdate{Status: "failed"}); !ok {
		t.Fatal("not applied to its command")
	}
	if a, _ := st.AgentSession(ctx, id); a.Status != "queued" {
		t.Fatalf("a stray command moved the session: %+v", a)
	}
	if _, ok, _ := st.ApplyReport(ctx, c2, "running", `{}`, AgentUpdate{Status: "running"}); !ok {
		t.Fatal("current turn's report not applied")
	}
	if a, _ := st.AgentSession(ctx, id); a.Status != "running" {
		t.Fatalf("%+v", a)
	}
}

func TestAdoptIsAtomic(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	a := AgentSession{Host: "laptop", Cwd: "/w", Provider: "codex", Perm: "acceptEdits", ProviderSession: "uuid-9"}
	ids := make(chan int64, 8)
	for range 8 {
		go func() {
			id, _, err := st.AdoptAgentSession(ctx, a)
			if err != nil {
				t.Error(err)
			}
			ids <- id
		}()
	}
	first := <-ids
	for range 7 {
		if id := <-ids; id != first {
			t.Fatalf("two rows: %d %d", first, id)
		}
	}
	var n int
	_ = st.DB.QueryRow(`SELECT count(*) FROM agent_sessions`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d rows", n)
	}
	// The unique index backs it up.
	if _, _, err := st.CreateAgentSession(ctx, a, nil); err == nil {
		t.Fatal("a second row for the same CLI session was accepted")
	}
	// Sessions without a CLI id yet don't collide.
	a.ProviderSession = ""
	for range 2 {
		if _, _, err := st.CreateAgentSession(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, ix := range []string{"agent_sessions_provider", "agent_sessions_review"} {
		var name string
		if err := st.DB.QueryRow(`SELECT name FROM sqlite_schema WHERE type = 'index' AND name = ?`, ix).Scan(&name); err != nil {
			t.Errorf("index %s: %v", ix, err)
		}
	}
}

func TestRunLogTail(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	var b strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if _, err := st.DB.Exec(`INSERT INTO runs (host, path, key, kind, started_at, log, log_bytes) VALUES ('h', '/l', 'agent-1', 'other', ?, ?, ?)`,
		Now(), b.String(), b.Len()); err != nil {
		t.Fatal(err)
	}
	tail, err := st.RunLogTail(ctx, 1, 100)
	if err != nil || len(tail) != 100 || !strings.HasSuffix(tail, "line 19999\n") {
		t.Fatalf("%q %v", tail, err)
	}
	if short, _ := st.RunLogTail(ctx, 1, 1<<30); short != b.String() {
		t.Fatal("a tail longer than the log isn't the whole log")
	}
	if _, err := st.RunLogTail(ctx, 99, 10); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
