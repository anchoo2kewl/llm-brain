package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func TestTcallAndTmd(t *testing.T) {
	for _, c := range []struct{ name, input, want string }{
		{"Bash", `{"command":"cd ~/x &&\n  gh issue list","description":"list"}`, "Bash(cd ~/x && gh issue list)"},
		{"Read", `{"file_path":"/a/main.go"}`, "Read(/a/main.go)"},
		{"exec_command", `{"cmd":"git status","workdir":"/x"}`, "Ran git status"},
		{"shell", `{"command":["bash","-lc","make test"]}`, "Ran make test"},
		{"apply_patch", "*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** End Patch", "apply_patch(a.go)"},
		{"TodoWrite", `{}`, "TodoWrite"},
	} {
		if got := tcall(c.name, c.input); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	got := string(tmd("# Plan\nUse **bold** and `go test` <script>x</script>\n```diff\n-old\n+new\n```"))
	for _, w := range []string{"<b># Plan</b>", "<b>bold</b>", `<span class="t-code">go test</span>`, "&lt;script&gt;", `<span class="t-del">-old</span>`, `<span class="t-add">+new</span>`} {
		if !strings.Contains(got, w) {
			t.Errorf("tmd lacks %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, "<script>") {
		t.Error("tmd let markup through")
	}
	if l := tlines("a\nb\nc\nd\ne\nf", 3); l.More != 3 || l.Head != "a\nb\nc" {
		t.Errorf("tlines %+v", l)
	}
	if l := tlines("a\nb\nc\nd", 3); l.More != 0 { // never "+1 lines"
		t.Errorf("tlines 4 %+v", l)
	}
}

func TestTerminalAndChatViews(t *testing.T) {
	r := newSessRig(t)
	now := store.Now()
	if _, err := r.st.DB.Exec(`INSERT INTO sessions (id, host, path, provider, session_uuid, cwd, title, file_mtime, first_at, message_count) VALUES
		(60, 'studio', '/c.jsonl', 'claude', 'u60', '/w', 'claude work', ?, ?, 4),
		(61, 'studio', '/x.jsonl', 'codex', 'u61', '/w', 'codex work', ?, ?, 4)`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	long := "l1\nl2\nl3\nl4\nl5\nl6\nl7"
	for _, m := range []struct {
		sess                            int64
		role, kind, text, tool, in, use string
	}{
		{60, "user", "text", "fix the pager", "", "", ""},
		{60, "assistant", "text", "On it. **First** run `gh issue list`.", "", "", ""},
		{60, "assistant", "tool_use", "", "Bash", `{"command":"gh issue list"}`, "t1"},
		{60, "user", "tool_result", long, "", "", "t1"},
		{61, "user", "text", "check status", "", "", ""},
		{61, "assistant", "tool_use", "", "exec_command", `{"cmd":"git status"}`, "c1"},
		{61, "user", "tool_result", "clean", "", "", "c1"},
		{61, "assistant", "text", "All clean.", "", "", ""},
	} {
		if _, err := r.st.DB.Exec(`INSERT INTO messages (session_id, line_off, idx, role, kind, ts, text, tool_name, tool_input, tool_use_id) VALUES (?, (SELECT count(*) FROM messages), 0, ?, ?, ?, ?, ?, ?, ?)`,
			m.sess, m.role, m.kind, now, m.text, m.tool, m.in, m.use); err != nil {
			t.Fatal(err)
		}
	}
	has := func(path string, cookie string, want ...string) string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, r.ts.URL+path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: viewCookie, Value: cookie})
		}
		resp, err := r.cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		body := string(raw)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s (cookie %q) lacks %q", path, cookie, w)
			}
		}
		return body
	}
	// Terminal is the default: the CLI's look, the toggle on the page.
	body := has("/sessions/60", "", `class="transcript tview p-claude`, `class="seg viewseg"`, `view=chat" data-view="chat"`,
		"<b>Bash(gh issue list)</b>", `class="g-res"`, "… +4 lines", "<b>First</b>", `<span class="t-code">gh issue list</span>`)
	if strings.Contains(body, `class="msg `) {
		t.Error("terminal view has chat cards")
	}
	has("/sessions/61", "", `p-codex`, "<b>Ran git status</b>", "clean")
	// Chat on the URL, or remembered in the cookie.
	for _, c := range []struct{ path, cookie string }{{"/sessions/60?view=chat", ""}, {"/sessions/60", "chat"}, {"/sessions/61?view=chat", "term"}} {
		b := has(c.path, c.cookie, `class="msg `, `data-view="chat"`)
		if strings.Contains(b, "tview") {
			t.Errorf("%s cookie %q: chat view has the terminal look", c.path, c.cookie)
		}
	}
	// Live updates come in the view asked for.
	has("/sessions/60/more?after=0&view=term", "", `class="t-row t-tool`)
	has("/sessions/60/more?after=0&view=chat", "", `class="msg `)
	// Embedded (a workspace pane): no toggle of its own, the pane has it.
	if b := has("/sessions/60?embed=1&view=term", "", "tview"); strings.Contains(b, `class="seg viewseg"`) {
		t.Error("an embedded transcript has its own toggle")
	}

	// A task (the chats view): the toggle, and its conversation in either view.
	form := url.Values{"with": {"agent:claude"}, "folder": {"studio|" + r.repo}, "perm": {"default"}, "prompt": {"Fix the pager"}}
	if code, loc := r.post(t, "/chats/new", form, r.ts.URL); code != http.StatusSeeOther || loc != "/agents/1" {
		t.Fatalf("start: %d %s", code, loc)
	}
	has("/agents/1", "", `class="seg viewseg"`, `tview p-claude`, `/agents/1/conversation?view=term`, `class="t-row t-user"`)
	has("/agents/1?view=chat", "", `/agents/1/conversation?view=chat`, `class="msg user"`)
	has("/agents/1/conversation?view=term", "", `class="t-row t-user"`)

	// A hub terminal: the toggle, and its transcript over it in Chat.
	if _, err := r.st.DB.Exec(`INSERT INTO terminals (id, host, provider, cwd, title, status, created_at, updated_at, session_uuid)
		VALUES (3, 'studio', 'claude', '/w', 'pager', 'live', ?, ?, 'u60')`, now, now); err != nil {
		t.Fatal(err)
	}
	has("/terminals/3", "", `class="seg viewseg"`)
	if b := has("/terminals/3?view=chat", "", `class="termchat" src="/sessions/60?embed=1&amp;view=chat"`); !strings.Contains(b, `id="term"`) {
		t.Error("the live terminal must stay on the page under its chat view")
	}
	has("/api/terminals", "", `"session_id":60`)
	// The workspace pane's toggle and chat view.
	has("/static/workspace.js", "", "ws-viewseg", `"/sessions/" + t.session_id + "?embed=1&view=chat"`, `"?embed=1&view=" + shownView`, "st.views[id]")
	has("/static/app.js", "", `hub_view=`, `localStorage.setItem("hub.view"`)
}
