package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
	"github.com/anchoo2kewl/llm-brain/hub/internal/ingest"
	"github.com/anchoo2kewl/llm-brain/hub/internal/llm"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// fakeGateway: the account gateway's models list and a streamed completion.
func fakeGateway(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-4-5","owned_by":"anthropic"},{"id":"gpt-5","owned_by":"openai"}]}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			for _, p := range []string{"Hello ", "**world** <b>x</b>"} {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", p)
				w.(http.Flusher).Flush()
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type sessRig struct {
	st    *store.Store
	srv   *Server
	ts    *httptest.Server
	repo  string
	chats *llm.Chats
	cl    *http.Client
}

func newSessRig(t *testing.T) *sessRig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := ingest.NewEngine(st, "acme", "acme/web-app")
	srv, err := New(st, eng, t.TempDir(), "studio", log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	gw := fakeGateway(t)
	chats := &llm.Chats{S: st, C: &llm.Client{URL: gw.URL, Key: "test-key"}}
	srv.Agents = &Sessions{Ctl: &agent.Control{S: st}, Hub: &agent.LocalHub{S: st}, Chats: chats}
	root := t.TempDir()
	repo := filepath.Join(root, "app")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	ctx := context.Background()
	if err := st.SetAgentInventory(ctx, "studio", "test", []string{root}, []store.AgentFolder{{Path: repo, Branch: "main"}}, []string{"claude", "codex"}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &sessRig{st: st, srv: srv, ts: ts, repo: repo, chats: chats, cl: cl}
}

func (r *sessRig) get(t *testing.T, path string) (int, string, http.Header) {
	t.Helper()
	resp, err := r.cl.Get(r.ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func (r *sessRig) post(t *testing.T, path string, form url.Values, origin string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := r.cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusSeeOther {
		return resp.StatusCode, resp.Header.Get("Location")
	}
	return resp.StatusCode, string(b)
}

func TestNewAgentChatAndPages(t *testing.T) {
	r := newSessRig(t)
	for _, p := range []string{"/chats", "/chats/new", "/agents", "/prompts", "/chats?q=x"} {
		if code, body, _ := r.get(t, p); code != 200 {
			t.Fatalf("%s: %d %s", p, code, body)
		}
	}
	_, body, _ := r.get(t, "/chats/new")
	for _, want := range []string{`value="agent:claude"`, `value="agent:codex"`, `label="Plain chat · Claude"`, `value="plain:gpt-5"`, `studio|` + r.repo} {
		if !strings.Contains(body, want) {
			t.Errorf("new chat page missing %s", want)
		}
	}
	if strings.Contains(body, `value="agent:gemini"`) {
		t.Error("offers a CLI no host has")
	}

	form := url.Values{"with": {"agent:claude"}, "folder": {"studio|" + r.repo}, "perm": {"default"}, "prompt": {"Fix <script>alert(1)</script> now"}}
	if code, _ := r.post(t, "/chats/new", form, "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-site POST: %d", code)
	}
	code, loc := r.post(t, "/chats/new", form, r.ts.URL)
	if code != http.StatusSeeOther || loc != "/agents/1" {
		t.Fatalf("start: %d %s", code, loc)
	}
	code, body, _ = r.get(t, "/agents/1")
	if code != 200 || strings.Contains(body, "<script>alert(1)") || !strings.Contains(body, "Fix &lt;script&gt;") || !strings.Contains(body, "Review with Codex") {
		t.Fatalf("agent page: %d\n%s", code, body)
	}
	_, _, h := r.get(t, "/agents/1/conversation")
	if h.Get("X-Status") != "queued" {
		t.Errorf("status %q", h.Get("X-Status"))
	}
	if last, _ := r.st.Setting(context.Background(), store.KeyChatLast); last != "agent:claude" {
		t.Errorf("last used %q", last)
	}

	// The host agent API: poll, then report as JSON only.
	code, body, _ = r.get(t, "/api/agent/commands?host=studio&version=t")
	var cmds []store.Command
	if code != 200 || json.Unmarshal([]byte(body), &cmds) != nil || len(cmds) != 1 || cmds[0].Kind != "turn" {
		t.Fatalf("poll: %d %s", code, body)
	}
	req, _ := http.NewRequest(http.MethodPost, r.ts.URL+"/api/agent/commands/1", strings.NewReader(`{"status":"running","pid":7,"session_id":"abc"}`))
	req.Header.Set("Content-Type", "text/plain")
	if resp, _ := r.cl.Do(req); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain report: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, r.ts.URL+"/api/agent/commands/1", strings.NewReader(`{"status":"running","pid":7,"session_id":"abc"}`))
	req.Header.Set("Content-Type", "application/json")
	if resp, _ := r.cl.Do(req); resp.StatusCode != 200 {
		t.Fatalf("report: %d", resp.StatusCode)
	}
	if a, _ := r.st.AgentSession(context.Background(), 1); a.Status != "running" || a.ProviderSession != "abc" {
		t.Fatalf("%+v", a)
	}
	// Folders outside the host's roots are refused before anything is queued.
	form.Set("folder", "studio|/etc")
	if code, body := r.post(t, "/chats/new", form, ""); code != 200 || !strings.Contains(body, "not a folder under") {
		t.Fatalf("outside folder: %d", code)
	}
	// Bypass needs the box.
	form.Set("folder", "studio|"+r.repo)
	form.Set("perm", "bypass")
	if code, body := r.post(t, "/chats/new", form, ""); code != 200 || !strings.Contains(body, "allow bypass") {
		t.Fatalf("bypass: %d", code)
	}

	// Not from the tailnet: refused.
	w := httptest.NewRecorder()
	rq := httptest.NewRequest(http.MethodGet, "/api/agent/commands?host=studio", nil)
	rq.RemoteAddr = "203.0.113.9:4000"
	r.srv.Handler().ServeHTTP(w, rq)
	if w.Code != http.StatusForbidden {
		t.Fatalf("off-tailnet poll: %d", w.Code)
	}
}

func TestPlainChat(t *testing.T) {
	r := newSessRig(t)
	code, loc := r.post(t, "/chats/new", url.Values{"with": {"plain:gpt-5"}, "prompt": {"hi"}}, "")
	if code != http.StatusSeeOther || loc != "/llms/1" {
		t.Fatalf("%d %s", code, loc)
	}
	// The page streams the reply: events end with the rendered HTML.
	_, ev, _ := r.get(t, "/llms/1/events?msg=2")
	if !strings.Contains(ev, "event: done") || !strings.Contains(ev, `\u003cstrong\u003eworld\u003c/strong\u003e`) {
		t.Fatalf("events:\n%s", ev)
	}
	r.chats.Wait()
	code, body, _ := r.get(t, "/llms/1")
	if code != 200 || !strings.Contains(body, "<strong>world</strong> &lt;b&gt;x&lt;/b&gt;") || !strings.Contains(body, ">gpt-5<") {
		t.Fatalf("%d\n%s", code, body)
	}
	// Switch models mid-conversation.
	if code, loc := r.post(t, "/llms/1/send", url.Values{"model": {"claude-sonnet-4-5"}, "prompt": {"again"}}, ""); code != http.StatusSeeOther || strings.Contains(loc, "err=") {
		t.Fatalf("%d %s", code, loc)
	}
	r.chats.Wait()
	ms, _ := r.st.LLMMessages(context.Background(), 1)
	if len(ms) != 4 || ms[3].Model != "claude-sonnet-4-5" || ms[3].Status != "done" {
		t.Fatalf("%+v", ms)
	}
	// Plain chats are in the chat list and its search.
	if _, body, _ := r.get(t, "/chats?q=world"); !strings.Contains(body, `href="/llms/1"`) {
		t.Error("search missed the plain chat")
	}
	if code, _, _ := r.get(t, "/llms/1/events?msg=999"); code != 404 {
		t.Errorf("events for another message: %d", code)
	}
}

func TestContinueIngestedSession(t *testing.T) {
	r := newSessRig(t)
	ctx := context.Background()
	line := `{"type":"user","sessionId":"5f0c2a8e-1111-4c2b-9a51-0d6c3b1f7a10","timestamp":"2026-10-14T20:00:00Z","cwd":"` + r.repo +
		`","message":{"role":"user","content":"hello"}}` + "\n"
	if _, err := r.srv.Engine.Chunk(ctx, ingest.Chunk{Host: "studio", Kind: ingest.KindClaude,
		Path: "/p/app/5f0c2a8e-1111-4c2b-9a51-0d6c3b1f7a10.jsonl", Mtime: time.Now(), Data: []byte(line)}); err != nil {
		t.Fatal(err)
	}
	code, body, _ := r.get(t, "/sessions/1/agent")
	if code != 200 || !strings.Contains(body, `action="/sessions/1/send"`) || !strings.Contains(body, "claude --resume 5f0c2a8e-1111-4c2b-9a51-0d6c3b1f7a10") {
		t.Fatalf("%d\n%s", code, body)
	}
	if _, body, _ := r.get(t, "/sessions/1"); !strings.Contains(body, `data-src="/sessions/1/agent"`) {
		t.Fatal("session page has no composer slot")
	}
	code, loc := r.post(t, "/sessions/1/send", url.Values{"prompt": {"carry on"}, "perm": {"acceptEdits"}}, "")
	if code != http.StatusSeeOther || loc != "/agents/1" {
		t.Fatalf("%d %s", code, loc)
	}
	cmds, _ := r.st.Commands(ctx, 1, 10)
	var tr agent.Turn
	_ = json.Unmarshal(cmds[0].Payload, &tr)
	if tr.Resume != "5f0c2a8e-1111-4c2b-9a51-0d6c3b1f7a10" || tr.Cwd != r.repo || tr.Prompt != "carry on" {
		t.Fatalf("%+v", tr)
	}
	// Review it with codex: a second session linked to the first.
	_ = r.st.UpdateAgentSession(ctx, 1, store.AgentUpdate{Status: "idle"})
	code, loc = r.post(t, "/sessions/1/review", url.Values{"base": {"main"}}, "")
	if code != http.StatusSeeOther || loc != "/agents/2" {
		t.Fatalf("%d %s", code, loc)
	}
	if a, _ := r.st.AgentSession(ctx, 2); a.Provider != "codex" || a.ReviewOf != 1 || a.Perm != agent.PermPlan {
		t.Fatalf("%+v", a)
	}
	// Once the review has findings, its page offers to send them back.
	res := "## Verdict: ISSUES"
	_ = r.st.UpdateAgentSession(ctx, 2, store.AgentUpdate{Status: "idle", LastResult: &res, ProviderSession: "0199"})
	if _, body, _ := r.get(t, "/agents/2"); !strings.Contains(body, `action="/agents/2/sendback"`) {
		t.Fatal("no send-back button")
	}
	if code, loc := r.post(t, "/agents/2/sendback", nil, ""); code != http.StatusSeeOther || loc != "/agents/1" {
		t.Fatalf("send back: %d %s", code, loc)
	}
	if a, _ := r.st.AgentSession(ctx, 1); a.Status != "queued" {
		t.Fatalf("%+v", a)
	}
}

func TestPromptLibrary(t *testing.T) {
	r := newSessRig(t)
	if code, loc := r.post(t, "/prompts", url.Values{"name": {"fix"}, "body": {"Fix {{ISSUE}} in {{REPO}}"}}, ""); code != http.StatusSeeOther || loc != "/prompts" {
		t.Fatalf("%d %s", code, loc)
	}
	_, body, _ := r.get(t, "/prompts")
	if !strings.Contains(body, "<code>ISSUE</code>") {
		t.Fatal(body)
	}
	if _, body, _ := r.get(t, "/chats/new?saved=1"); !strings.Contains(body, "Fix {{ISSUE}} in {{REPO}}</textarea>") {
		t.Fatal("saved prompt not prefilled")
	}
	// A missing variable is refused with the names.
	code, body := r.post(t, "/chats/new", url.Values{"with": {"agent:claude"}, "folder": {"studio|" + r.repo}, "prompt": {"Fix {{ISSUE}} in {{REPO}}"}}, "")
	if code != 200 || !strings.Contains(body, "ISSUE") {
		t.Fatalf("%d", code)
	}
	if code, loc := r.post(t, "/chats/new", url.Values{"with": {"agent:claude"}, "folder": {"studio|" + r.repo},
		"prompt": {"Fix {{ISSUE}} in {{REPO}}"}, "vars": {"ISSUE=#12"}}, ""); code != http.StatusSeeOther {
		t.Fatalf("%d %s", code, loc)
	}
	if a, _ := r.st.AgentSession(context.Background(), 1); a.Title != "Fix #12 in app" {
		t.Fatalf("%+v", a)
	}
	if code, _ := r.post(t, "/prompts/1/delete", nil, ""); code != http.StatusSeeOther {
		t.Fatal(code)
	}
}
