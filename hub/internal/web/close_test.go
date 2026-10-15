package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// procTable is a fake process table: one claude in /w that ends on TERM.
type procTable struct {
	mu   sync.Mutex
	dead bool
	sent []syscall.Signal
}

func (p *procTable) List(context.Context) ([]agent.Proc, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return nil, nil
	}
	return []agent.Proc{{PID: 4242, PPID: 1, Args: "/opt/homebrew/bin/claude"}}, nil
}
func (p *procTable) OpenedBy(context.Context, string) []int { return nil }
func (p *procTable) Cwd(context.Context, int) string        { return "/w" }
func (p *procTable) Signal(_ int, s syscall.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, s)
	p.dead = true
	return nil
}
func (p *procTable) Alive(int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.dead
}

func TestCloseEverywhere(t *testing.T) {
	r := newSessRig(t)
	ctx := context.Background()
	t.Setenv("HOME", t.TempDir()) // no Claude pid files: found by folder
	procs := &procTable{}
	ex := &agent.Executor{Host: "studio", Version: "test", Hub: r.srv.Agents.Hub, FindTools: func(context.Context) []store.ToolInfo { return nil },
		StateDir: t.TempDir(), Providers: []string{}, Procs: procs}
	if err := ex.PollOnce(ctx); err != nil { // the host agent is live
		t.Fatal(err)
	}
	now := store.Now()
	if _, err := r.st.DB.Exec(`INSERT INTO sessions (id, host, path, provider, session_uuid, cwd, title, file_mtime, first_at, message_count, originator) VALUES
		(50, 'studio', '/s/a.jsonl', 'claude', '40f66c98-049f-488c-944d-6d3d5b163eed', '/w', 'outside session', ?, ?, 3, ''),
		(51, 'studio', '/s/b.jsonl', 'claude', 'u51', '/w', 'headless run', ?, ?, 3, 'sdk-cli'),
		(52, 'studio', '/s/c.jsonl', 'codex', 'u52', '/t', 'terminal session', ?, ?, 3, '')`, now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.DB.Exec(`INSERT INTO terminals (id, host, provider, cwd, title, status, created_at, updated_at, session_uuid)
		VALUES (7, 'studio', 'codex', '/t', 'my terminal', 'live', ?, ?, 'u52')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.DB.Exec(`INSERT INTO messages (session_id, line_off, idx, role, kind, ts, text) VALUES (50, 0, 0, 'assistant', 'text', ?, 'Done. Run the tests?')`,
		store.T(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.DB.Exec(`UPDATE sessions SET file_mtime = ? WHERE id = 50`, store.T(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}

	// Close is offered on every page that shows a session or a terminal. The inbox first: opening a session
	// marks it seen.
	for _, c := range []struct {
		path string
		want []string
	}{
		{"/inbox", []string{`action="/sessions/50/close"`}},
		{"/now", []string{`action="/terminals/7/kill"`, "Close terminal"}},
		{"/sessions", []string{`action="/sessions/50/close"`}},
		{"/sessions/50", []string{`action="/sessions/50/close"`, "Close session"}},
		{"/sessions/52", []string{`action="/terminals/7/kill"`, "hub terminal"}},
		{"/chats", []string{`action="/terminals/7/kill"`}},
		{"/terminals/7", []string{`action="/terminals/7/kill"`, "Close terminal"}},
		{"/workspace", []string{`/static/workspace.js`}},
		{"/static/workspace.js", []string{`"/api/sessions/" + t.sid + "/close"`, `"/api/terminals/" + id + "/close"`, "Close?"}},
	} {
		path, want := c.path, c.want
		code, body, _ := r.get(t, path)
		if code != 200 {
			t.Fatalf("%s: %d", path, code)
		}
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s lacks %q", path, w)
			}
		}
	}
	if _, body, _ := r.get(t, "/sessions/51"); strings.Contains(body, "/sessions/51/close") {
		t.Error("a headless run offers Close")
	}

	// Another site can't close anything.
	if code, _ := r.post(t, "/sessions/50/close", nil, "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-site close: %d", code)
	}
	// The API refuses a headless run, with the reason.
	resp, err := r.cl.Post(r.ts.URL+"/api/sessions/51/close", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 || !strings.Contains(string(b), "headless") {
		t.Fatalf("headless: %d %s", resp.StatusCode, b)
	}
	// A hub terminal's session closes as the terminal.
	resp, _ = r.cl.Post(r.ts.URL+"/api/sessions/52/close", "application/json", strings.NewReader("{}"))
	var out closeOut
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Closing != "terminal" || out.ID != 7 {
		t.Fatalf("terminal session: %+v", out)
	}
	// From the Now page: back to the Now page, queued for the Mac.
	code, loc := r.post(t, "/sessions/50/close", url.Values{"back": {"/now"}}, "")
	if code != http.StatusSeeOther || loc != "/now" {
		t.Fatalf("close: %d %s", code, loc)
	}
	if _, body, _ := r.get(t, "/sessions/50"); !strings.Contains(body, "closing…") {
		t.Error("the session page doesn't show it closing")
	}
	if err := ex.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	ex.Wait()
	if len(procs.sent) != 1 || procs.sent[0] != syscall.SIGTERM {
		t.Fatalf("signals %v", procs.sent)
	}
	var st struct {
		Closed bool               `json:"closed"`
		Status string             `json:"status"`
		Result *agent.CloseResult `json:"result"`
	}
	_, body, _ := r.get(t, "/api/sessions/50/close")
	if err := json.Unmarshal([]byte(body), &st); err != nil || !st.Closed || st.Status != "done" || st.Result == nil || st.Result.PIDs[0] != 4242 {
		t.Fatalf("state %s", body)
	}
	if _, err := r.st.DB.Exec(`DELETE FROM inbox_seen`); err != nil { // not hidden because it was seen
		t.Fatal(err)
	}
	if _, body, _ := r.get(t, "/inbox"); strings.Contains(body, "/sessions/50") {
		t.Error("a closed session is still waiting in the inbox")
	}
	if _, body, _ := r.get(t, "/sessions/50"); !strings.Contains(body, "closed") || strings.Contains(body, `action="/sessions/50/close"`) {
		t.Error("the session page still offers Close")
	}
}
