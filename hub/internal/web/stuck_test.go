package web

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func TestFrozenTerminalEverywhere(t *testing.T) {
	r := newSessRig(t)
	ctx := context.Background()
	now := store.Now()
	since := store.T(time.Now().Add(-15 * time.Minute))
	blk, _ := json.Marshal(store.TermBlock{Reason: "macOS privacy prompt", PID: 48777, Proc: "claude --dangerously-skip-permissions --session-id 49713123-…",
		Func: "openat", Path: "/Users/anshuman/Documents", Since: since, FDA: []string{"/Users/anshuman/projects/llm-brain/bin/hub", "/opt/homebrew/bin/tmux"}})
	uuid := "49713123-0779-46b0-a028-d1bae215b9d3"
	if _, err := r.st.DB.Exec(`INSERT INTO terminals (id, host, provider, cwd, title, status, created_at, updated_at, live_at, session_uuid, blocked) VALUES
		(18, 'studio', 'claude', '/Users/anshuman/play/learn', 'Learn design', 'live', ?, ?, ?, ?, ?),
		(19, 'studio', 'shell', '/w', 'a shell', 'live', ?, ?, ?, '', '')`, now, now, now, uuid, string(blk), now, now, now); err != nil {
		t.Fatal(err)
	}
	// Its transcript exists, so a restart continues it.
	if _, err := r.st.DB.Exec(`INSERT INTO sessions (id, host, path, provider, session_uuid, cwd, title, file_mtime, first_at, message_count, originator)
		VALUES (60, 'studio', '/s/learn.jsonl', 'claude', ?, '/Users/anshuman/play/learn', 'Learn design', ?, ?, 4, '')`, uuid, since, since); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		path string
		want []string
	}{
		{"/inbox", []string{"Terminal 18 is waiting on a macOS permission prompt on studio", `action="/terminals/18/restart"`, "Full Disk Access", "Restart CLI…"}},
		{"/inbox?state=blocked", []string{"Terminal 18 is waiting"}},
		{"/now", []string{"waiting on a macOS permission prompt", `chip s-blocked`, `action="/terminals/18/restart"`}},
		{"/terminals/18", []string{`id="termstatus">blocked`, "Waiting on a macOS permission prompt on studio", `href="vnc://studio"`,
			"/opt/homebrew/bin/tmux", "/Users/anshuman/projects/llm-brain/bin/hub", "<code>openat</code>", "/Users/anshuman/Documents",
			`action="/terminals/18/restart"`, `name="back" value="/terminals/18"`}},
		{"/terminals/19", []string{`id="termstuck"></div>`}}, // not frozen: no notice
		{"/static/workspace.js", []string{`"/api/terminals/" + id + "/restart"`, "Restart?", `i.blocked`}},
		{"/static/term.js", []string{`getElementById("termstuck")`}},
	} {
		code, body, _ := r.get(t, c.path)
		if code != 200 {
			t.Fatalf("%s: %d", c.path, code)
		}
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s lacks %q", c.path, w)
			}
		}
	}
	if _, body, _ := r.get(t, "/terminals/19"); strings.Contains(body, "/terminals/19/restart") {
		t.Error("a shell offers Restart CLI")
	}

	// The workspace's list and the terminal's status carry it, with the fix in a sentence.
	_, body, _ := r.get(t, "/api/terminals")
	var ts []termJSON
	_ = json.Unmarshal([]byte(body), &ts)
	var got *termJSON
	for i := range ts {
		if ts[i].ID == 18 {
			got = &ts[i]
		} else if ts[i].Blocked != nil {
			t.Errorf("terminal %d blocked", ts[i].ID)
		}
	}
	if got == nil || got.Blocked == nil || got.Blocked.Func != "openat" || !strings.Contains(got.Fix, "vnc://studio") || !strings.Contains(got.Fix, "Full Disk Access") {
		t.Fatalf("api: %+v", got)
	}
	if _, body, _ := r.get(t, "/terminals/18/status"); !strings.Contains(body, `"blocked":{"reason":"macOS privacy prompt"`) {
		t.Fatalf("status: %s", body)
	}

	// One inbox item, blocked — not also "waiting for you" — and its push says what happened.
	items := inboxKeys(t, r)
	it, ok := items["tb:18:"+since]
	if !ok || it.State != "blocked" || it.Restart != "/terminals/18/restart" {
		t.Fatalf("inbox %v", keys(items))
	}
	if _, dup := items["t:18"]; dup {
		t.Error("a frozen terminal is also waiting for you")
	}
	if n := noticeFor(it); n.Title != "Terminal 18 is waiting on a macOS permission prompt on studio" || n.URL != "/terminals/18" || !strings.Contains(n.Body, "Allow") {
		t.Fatalf("notice %+v", n)
	}

	// Restart: another site can't; a shell can't; from the page it queues a restart that resumes the transcript.
	if code, _ := r.post(t, "/terminals/18/restart", nil, "https://evil.example"); code != 403 {
		t.Fatalf("cross-site restart: %d", code)
	}
	resp, err := r.cl.Post(r.ts.URL+"/api/terminals/19/restart", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 || !strings.Contains(string(b), "shell") {
		t.Fatalf("shell restart: %d %s", resp.StatusCode, b)
	}
	code, loc := r.post(t, "/terminals/18/restart", url.Values{"back": {"/inbox"}}, "")
	if code != 303 || loc != "/inbox" {
		t.Fatalf("restart: %d %s", code, loc)
	}
	var cid int64
	if err := r.st.DB.QueryRow(`SELECT id FROM commands WHERE kind = 'term_restart' AND agent_id = 18`).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	c, _ := r.st.Command(ctx, cid)
	var o agent.TermOpen
	_ = json.Unmarshal(c.Payload, &o)
	if o.Provider != "claude" || o.Resume != uuid || o.SessionID != "" {
		t.Fatalf("restart payload %+v", o)
	}
	// The host restarted it: no longer blocked anywhere.
	if err := r.srv.Agents.Hub.Report(ctx, cid, agent.Report{Status: "done"}); err != nil {
		t.Fatal(err)
	}
	if _, body, _ := r.get(t, "/now"); strings.Contains(body, "macOS permission prompt") {
		t.Error("still blocked on Now after the restart")
	}
	if _, ok := inboxKeys(t, r)["tb:18:"+since]; ok {
		t.Error("still in the inbox after the restart")
	}
}
