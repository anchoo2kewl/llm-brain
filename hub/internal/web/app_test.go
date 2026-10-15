package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
	"github.com/anchoo2kewl/llm-brain/hub/internal/ingest"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func TestStateLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"running": "working", "live": "working", "queued": "working", "waiting": "waiting", "review": "waiting",
		"blocked": "blocked", "succeeded": "done", "idle": "done", "merged": "done", "failed": "failed",
		"stopped": "failed", "FAILING": "failed", "whatever": "idle",
	} {
		if got := state(in); got != want {
			t.Errorf("state(%q) = %q, want %q", in, got, want)
		}
	}
	if stateWord("waiting") != "waiting for you" || stateWord("queued") != "queued" || stateWord("succeeded") != "done" {
		t.Error("stateWord")
	}
}

func inboxKeys(t *testing.T, r *sessRig) map[string]Item {
	t.Helper()
	code, body, _ := r.get(t, "/api/inbox")
	if code != 200 {
		t.Fatalf("/api/inbox: %d %s", code, body)
	}
	var in Inbox
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatal(err)
	}
	out := map[string]Item{}
	for _, it := range in.Items {
		out[it.Key] = it
	}
	return out
}

func TestInboxCollectsWhatNeedsYou(t *testing.T) {
	r := newSessRig(t)
	ctx := context.Background()
	// A hub task whose answer is ready, and one that failed.
	a1, _, err := r.st.CreateAgentSession(ctx, store.AgentSession{Host: "studio", Provider: "claude", Title: "add pager", Status: "idle"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := "Added the pager. Shall I open a PR?"
	idle := "idle"
	_ = r.st.UpdateAgentSession(ctx, a1, store.AgentUpdate{Status: idle, LastResult: &res})
	a2, _, _ := r.st.CreateAgentSession(ctx, store.AgentSession{Host: "studio", Provider: "codex", Title: "port it", Status: "idle"}, nil)
	msg := "exit 1"
	_ = r.st.UpdateAgentSession(ctx, a2, store.AgentUpdate{Status: "failed", Error: &msg})
	_, _ = r.st.DB.Exec(`UPDATE agent_sessions SET turns = 1`)

	// A Claude session (not the hub's) whose last word is the assistant's, quiet for a minute: waiting.
	// And one that's mid-tool-call: not waiting.
	eng := ingest.NewEngine(r.st, "acme", "acme/web-app")
	put := func(uuid string, lines ...string) {
		t.Helper()
		data := strings.Join(lines, "\n") + "\n"
		if _, err := eng.Chunk(ctx, ingest.Chunk{Host: "laptop", Kind: ingest.KindClaude, Path: "/p/x/" + uuid + ".jsonl",
			Mtime: time.Now().Add(-time.Minute), Data: []byte(data)}); err != nil {
			t.Fatal(err)
		}
	}
	user := func(uuid, text string) string {
		return `{"type":"user","sessionId":"` + uuid + `","timestamp":"2026-10-15T10:00:00Z","cwd":"/w/app","message":{"role":"user","content":"` + text + `"}}`
	}
	put("11111111-1111-1111-1111-111111111111", user("11111111-1111-1111-1111-111111111111", "fix it"),
		`{"type":"assistant","sessionId":"11111111-1111-1111-1111-111111111111","timestamp":"2026-10-15T10:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"Fixed. Run the tests?"}]}}`)
	put("22222222-2222-2222-2222-222222222222", user("22222222-2222-2222-2222-222222222222", "go"),
		`{"type":"assistant","sessionId":"22222222-2222-2222-2222-222222222222","timestamp":"2026-10-15T10:01:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"make"}}]}}`)

	// The fleet: a blocked issue and a PR that needs a review.
	status := `{"host":"studio","at":"2026-10-15T10:00:00Z","max_in_flight":2,
	  "owned":[{"number":12,"title":"Wire SSO","url":"https://github.com/acme/web/issues/12","author":{"login":"sam"},"labels":[{"name":"agent:blocked"}],"updatedAt":"2026-10-15T09:00:00Z"}],
	  "prs":[{"url":"https://github.com/acme/web/pull/40","title":"Pager","number":40,"reviewDecision":"REVIEW_REQUIRED","ci":"passing","updatedAt":"2026-10-15T09:30:00Z"},
	         {"url":"https://github.com/acme/web/pull/41","title":"Done one","number":41,"reviewDecision":"APPROVED","ci":"passing","updatedAt":"2026-10-15T09:30:00Z"}]}`
	if err := os.WriteFile(filepath.Join(r.srv.StateDir, "status.json"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}

	got := inboxKeys(t, r)
	sid, _ := r.st.SessionByUUID(ctx, "laptop", "11111111-1111-1111-1111-111111111111")
	want := map[string]string{"a:" + itoa(a1): "waiting", "a:" + itoa(a2): "failed", "s:" + itoa(sid): "waiting",
		"i:https://github.com/acme/web/issues/12": "blocked", "p:https://github.com/acme/web/pull/40": "review"}
	for k, st := range want {
		if got[k].State != st {
			t.Errorf("%s: state %q, want %q (have %v)", k, got[k].State, st, keys(got))
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected items: %v", keys(got))
	}
	if it := got["a:"+itoa(a1)]; it.ReplyTo != "/agents/"+itoa(a1)+"/send" || it.Detail != res {
		t.Errorf("task item %+v", it)
	}

	// Dismissing hides an item; opening a task's page does too.
	code, loc := r.post(t, "/inbox/seen", url.Values{"key": {"p:https://github.com/acme/web/pull/40"}}, "")
	if code != http.StatusSeeOther || loc != "/inbox" {
		t.Fatalf("dismiss: %d %s", code, loc)
	}
	if code, _ := r.post(t, "/inbox/seen", url.Values{"key": {"x"}}, "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-site dismiss: %d", code)
	}
	r.get(t, "/agents/"+itoa(a1))
	got = inboxKeys(t, r)
	if _, ok := got["p:https://github.com/acme/web/pull/40"]; ok {
		t.Error("dismissed PR still there")
	}
	if _, ok := got["a:"+itoa(a1)]; ok {
		t.Error("opened task still there")
	}
	// Something newer brings it back.
	time.Sleep(1100 * time.Millisecond)
	res2 := "Opened the PR."
	_ = r.st.UpdateAgentSession(ctx, a1, store.AgentUpdate{Status: idle, LastResult: &res2})
	if it, ok := inboxKeys(t, r)["a:"+itoa(a1)]; !ok || it.Detail != res2 {
		t.Errorf("a newer answer should come back: %+v", it)
	}

	// The page renders every item, escaped, with the quick replies.
	code, body, _ := r.get(t, "/inbox")
	if code != 200 || !strings.Contains(body, "Wire SSO") || !strings.Contains(body, `data-reply`) || !strings.Contains(body, "Fixed. Run the tests?") {
		t.Fatalf("/inbox: %d", code)
	}
	if code, body, _ := r.get(t, "/inbox?state=blocked"); code != 200 || strings.Contains(body, "Fixed. Run the tests?") {
		t.Fatal("filter didn't filter")
	}
	// The home page is the inbox, unless the fleet overview is picked.
	if _, body, _ := r.get(t, "/"); !strings.Contains(body, "<h1>Inbox</h1>") {
		t.Fatal("home isn't the inbox")
	}
	r.post(t, "/settings/app", url.Values{"home": {"fleet"}}, "")
	if _, body, _ := r.get(t, "/"); strings.Contains(body, "<h1>Inbox</h1>") {
		t.Fatal("home should be the fleet overview now")
	}
}

func keys(m map[string]Item) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestUpdatesPage(t *testing.T) {
	r := newSessRig(t)
	ctx := context.Background()
	_ = r.st.SetAgentTools(ctx, "studio", []store.ToolInfo{{Name: "claude", Version: "2.1.0", Method: "cask:claude-code"}, {Name: "gh", Version: "2.8"}})
	code, body, _ := r.get(t, "/settings/updates")
	if code != 200 || !strings.Contains(body, "cask:claude-code") || !strings.Contains(body, "installed some other way") {
		t.Fatalf("/settings/updates: %d\n%s", code, body)
	}
	code, loc := r.post(t, "/settings/updates", url.Values{"auto": {"1"}, "hour": {"6"}, "tool": {"claude", "codex"}, "host": {"studio", "laptop"}, "daily": {"studio"}}, "")
	if code != http.StatusSeeOther || !strings.Contains(loc, "saved=1") {
		t.Fatalf("save: %d %s", code, loc)
	}
	set, _ := agent.LoadUpdateSettings(ctx, r.st)
	if !set.Auto || set.Hour != 6 || strings.Join(set.Tools, ",") != "claude,codex" || strings.Join(set.Skip, ",") != "laptop" {
		t.Fatalf("saved %+v", set)
	}
	if code, _ := r.post(t, "/settings/updates", url.Values{"auto": {"1"}}, "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-site save: %d", code)
	}
	code, loc = r.post(t, "/settings/updates/run", url.Values{"host": {"studio"}, "tool": {"claude"}}, "")
	if code != http.StatusSeeOther || !strings.Contains(loc, "queued=studio") {
		t.Fatalf("run: %d %s", code, loc)
	}
	code, loc = r.post(t, "/settings/updates/run", url.Values{"host": {"studio"}}, "")
	if !strings.Contains(loc, "already+updating") {
		t.Fatalf("second run: %d %s", code, loc)
	}
	cs, _ := r.st.ToolUpdates(ctx, 5)
	if len(cs) != 1 || cs[0].Host != "studio" {
		t.Fatalf("commands %+v", cs)
	}
	if _, body, _ := r.get(t, "/now"); !strings.Contains(body, "Updating tools") {
		t.Error("/now should show the running update")
	}
	// A failed update lands in the inbox.
	rep, _ := json.Marshal(agent.Report{Status: "failed", Error: "failed: claude"})
	if err := r.srv.Agents.Hub.Report(ctx, cs[0].ID, agent.Report{Status: "failed", Error: "failed: claude"}); err != nil {
		t.Fatal(err)
	}
	_ = rep
	if it, ok := inboxKeys(t, r)["u:studio"]; !ok || it.State != "failed" || it.Detail != "failed: claude" {
		t.Fatalf("update failure in the inbox: %+v", it)
	}
}

// Every new page fits the CSP (no inline script, style or handler) and the shell is installable.
func TestAppShellFitsTheCSP(t *testing.T) {
	r := newSessRig(t)
	inlineScript := regexp.MustCompile(`(?i)<script(\s[^>]*)?>`)
	handler := regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	style := regexp.MustCompile(`(?i)\sstyle\s*=|<style`)
	for _, p := range []string{"/", "/inbox", "/now", "/settings", "/settings/updates", "/offline", "/fleet", "/chats"} {
		code, body, _ := r.get(t, p)
		if code != 200 {
			t.Fatalf("%s: %d", p, code)
		}
		for _, m := range inlineScript.FindAllString(body, -1) {
			if !strings.Contains(m, `src="/static/`) {
				t.Errorf("%s: inline script %s", p, m)
			}
		}
		if m := handler.FindString(body); m != "" {
			t.Errorf("%s: inline handler %q", p, m)
		}
		if m := style.FindString(body); m != "" {
			t.Errorf("%s: inline style %q", p, m)
		}
		for _, want := range []string{`rel="manifest"`, `class="tabbar"`, `data-count="inbox"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %s", p, want)
			}
		}
	}
	_, man, h := r.get(t, "/manifest.webmanifest")
	var m struct {
		Display string `json:"display"`
		Icons   []struct {
			Src, Purpose string
		} `json:"icons"`
	}
	if err := json.Unmarshal([]byte(man), &m); err != nil || m.Display != "standalone" || len(m.Icons) < 3 {
		t.Fatalf("manifest %v %+v", err, m)
	}
	for _, ic := range m.Icons {
		if code, _, _ := r.get(t, ic.Src); code != 200 {
			t.Errorf("icon %s: %d", ic.Src, code)
		}
	}
	if !strings.Contains(h.Get("Content-Type"), "manifest") {
		t.Errorf("manifest type %s", h.Get("Content-Type"))
	}
	_, _, h = r.get(t, "/sw.js")
	if h.Get("Service-Worker-Allowed") != "/" || !strings.Contains(h.Get("Content-Type"), "javascript") {
		t.Errorf("sw.js headers %v", h)
	}
}

func TestEventStream(t *testing.T) {
	r := newSessRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.srv.RunEvents(ctx, 50*time.Millisecond)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type %s", resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	next := func() Snapshot {
		t.Helper()
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var s Snapshot
				if err := json.Unmarshal([]byte(d), &s); err != nil {
					t.Fatal(err)
				}
				return s
			}
		}
		t.Fatal("stream ended")
		return Snapshot{}
	}
	first := next()
	if first.Inbox != 0 {
		t.Fatalf("first %+v", first)
	}
	a, _, _ := r.st.CreateAgentSession(context.Background(), store.AgentSession{Host: "studio", Provider: "claude", Title: "t", Status: "queued"}, nil)
	_ = a
	if s := next(); s.Now.Tasks != 1 {
		t.Fatalf("after a queued task: %+v", s)
	}
}
