package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
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
	got, err := st.AgentSessionByProvider(ctx, "studio", "uuid-1")
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
