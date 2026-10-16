package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// fakeProcs is a process table: processes die on the first signal in dieOn (default TERM).
type fakeProcs struct {
	mu     sync.Mutex
	procs  []Proc
	cwd    map[int]string
	opened map[string][]int
	dieOn  map[int]syscall.Signal
	dead   map[int]bool
	sent   []string
}

func (f *fakeProcs) List(context.Context) ([]Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Proc
	for _, p := range f.procs {
		if !f.dead[p.PID] {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakeProcs) OpenedBy(_ context.Context, path string) []int { return f.opened[path] }
func (f *fakeProcs) Cwd(_ context.Context, pid int) string         { return f.cwd[pid] }
func (f *fakeProcs) Signal(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, itoaInt(pid)+":"+sig.String())
	want, ok := f.dieOn[pid]
	if !ok {
		want = syscall.SIGTERM
	}
	if sig == want || sig == syscall.SIGKILL {
		if f.dead == nil {
			f.dead = map[int]bool{}
		}
		f.dead[pid] = true
	}
	return nil
}
func (f *fakeProcs) Alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.procs {
		if p.PID == pid {
			return !f.dead[pid]
		}
	}
	return false
}

func itoaInt(n int) string { return strconv.Itoa(n) }
func jsonInt(n int) string { return strconv.Itoa(n) }

const testUUID = "40f66c98-049f-488c-944d-6d3d5b163eed"

func closeExec(t *testing.T, pt *fakeProcs) (*Executor, *updHub) {
	t.Helper()
	old := closeWait
	closeWait = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond}
	t.Cleanup(func() { closeWait = old })
	hub := &updHub{}
	ex := &Executor{Host: "studio", Hub: hub, StateDir: t.TempDir(), Providers: []string{}, FindTools: noTools, Procs: pt}
	ex.init()
	return ex, hub
}

func runClose(t *testing.T, ex *Executor, hub *updHub, p SessionClose) (Report, CloseResult) {
	t.Helper()
	b, _ := json.Marshal(p)
	ex.sessionClose(context.Background(), store.Command{ID: 9, Kind: KindSessionClose, Payload: b})
	if len(hub.reps) == 0 {
		t.Fatal("no report")
	}
	r := hub.reps[len(hub.reps)-1]
	var cr CloseResult
	_ = json.Unmarshal([]byte(r.Result), &cr)
	return r, cr
}

func TestCloseClaudeByPidFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0o755)
	write := func(pid int, start string) {
		b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": testUUID, "procStart": start, "kind": "interactive", "entrypoint": "cli"})
		_ = os.WriteFile(filepath.Join(home, ".claude", "sessions", jsonInt(pid)+".json"), b, 0o600)
	}
	write(4242, "Thu Oct  1 17:52:32 2026") // ps pads the day; compared without the extra space
	write(77, "Mon Sep  1 09:00:00 2026")   // a stale file whose pid now belongs to another claude
	pt := &fakeProcs{procs: []Proc{
		{PID: 4242, PPID: 300, Start: "Thu Oct 1 17:52:32 2026", Args: "/opt/homebrew/bin/claude --resume"},
		{PID: 77, PPID: 300, Start: "Thu Oct 15 08:00:00 2026", Args: "/Users/me/.local/share/claude/versions/2.1.3"},
		{PID: 300, PPID: 1, Start: "x", Args: "-zsh"},
	}}
	ex, hub := closeExec(t, pt)
	r, cr := runClose(t, ex, hub, SessionClose{SessionID: 5, Provider: "claude", UUID: testUUID, Cwd: "/w"})
	if r.Status != "done" || cr.How != "pidfile" || len(cr.PIDs) != 1 || cr.PIDs[0] != 4242 || cr.Signal != "TERM" {
		t.Fatalf("report %+v result %+v", r, cr)
	}
	if strings.Join(pt.sent, ",") != "4242:terminated" {
		t.Fatalf("signals %v", pt.sent)
	}
	if hub.reps[0].Status != "running" {
		t.Fatalf("first report %+v", hub.reps[0])
	}
}

func TestCloseCodexByOpenFileThenStronger(t *testing.T) {
	pt := &fakeProcs{procs: []Proc{
		{PID: 500, PPID: 1, Args: "node /opt/homebrew/bin/codex"},
		{PID: 501, PPID: 500, Args: "/opt/homebrew/lib/node_modules/@openai/codex/bin/codex-aarch64-apple-darwin"},
		{PID: 600, PPID: 1, Args: "vim /x/rollout.jsonl"}, // also has it open: not a CLI, never touched
	}, opened: map[string][]int{"/x/rollout.jsonl": {501, 600}}, dieOn: map[int]syscall.Signal{501: syscall.SIGHUP}}
	ex, hub := closeExec(t, pt)
	r, cr := runClose(t, ex, hub, SessionClose{Provider: "codex", UUID: "u", Path: "/x/rollout.jsonl", Cwd: "/w"})
	if r.Status != "done" || cr.How != "open-file" || cr.Signal != "HUP" || len(cr.PIDs) != 1 || cr.PIDs[0] != 501 {
		t.Fatalf("report %+v result %+v", r, cr)
	}
	for _, s := range pt.sent {
		if strings.HasPrefix(s, "600:") {
			t.Fatalf("signalled vim: %v", pt.sent)
		}
	}
}

func TestCloseByCwdRefusals(t *testing.T) {
	t.Run("ambiguous", func(t *testing.T) {
		pt := &fakeProcs{procs: []Proc{{PID: 10, PPID: 1, Args: "gemini"}, {PID: 11, PPID: 1, Args: "node /usr/local/lib/node_modules/@google/gemini-cli/dist/index.js"}},
			cwd: map[int]string{10: "/w", 11: "/w"}}
		ex, hub := closeExec(t, pt)
		r, _ := runClose(t, ex, hub, SessionClose{Provider: "gemini", Cwd: "/w"})
		if r.Status != "failed" || !strings.Contains(r.Error, "ambiguous: 2") || len(pt.sent) != 0 {
			t.Fatalf("%+v sent %v", r, pt.sent)
		}
	})
	t.Run("headless", func(t *testing.T) {
		pt := &fakeProcs{procs: []Proc{{PID: 10, PPID: 1, Args: "claude -p --output-format stream-json fix it"}}, cwd: map[int]string{10: "/w"}}
		ex, hub := closeExec(t, pt)
		r, _ := runClose(t, ex, hub, SessionClose{Provider: "claude", UUID: testUUID, Cwd: "/w"})
		if r.Status != "failed" || !strings.Contains(r.Error, "headless") || len(pt.sent) != 0 {
			t.Fatalf("%+v sent %v", r, pt.sent)
		}
	})
	t.Run("codex exec", func(t *testing.T) {
		pt := &fakeProcs{procs: []Proc{{PID: 10, PPID: 1, Args: "codex exec --json fix"}}, cwd: map[int]string{10: "/w"}}
		ex, hub := closeExec(t, pt)
		r, _ := runClose(t, ex, hub, SessionClose{Provider: "codex", Cwd: "/w"})
		if r.Status != "failed" || !strings.Contains(r.Error, "headless") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("the hub runs inside it", func(t *testing.T) {
		me := os.Getpid()
		pt := &fakeProcs{procs: []Proc{{PID: 20, PPID: 1, Args: "claude"}, {PID: me, PPID: 20, Args: "hub serve"}}, cwd: map[int]string{20: "/w"}}
		ex, hub := closeExec(t, pt)
		r, _ := runClose(t, ex, hub, SessionClose{Provider: "claude", Cwd: "/w"})
		if r.Status != "failed" || !strings.Contains(r.Error, "inside it") || len(pt.sent) != 0 {
			t.Fatalf("%+v sent %v", r, pt.sent)
		}
	})
	t.Run("not running", func(t *testing.T) {
		pt := &fakeProcs{procs: []Proc{{PID: 10, PPID: 1, Args: "codex"}}, cwd: map[int]string{10: "/elsewhere"}}
		ex, hub := closeExec(t, pt)
		r, cr := runClose(t, ex, hub, SessionClose{Provider: "codex", Cwd: "/w"})
		if r.Status != "done" || cr.Note != "not running" || len(pt.sent) != 0 {
			t.Fatalf("%+v %+v", r, cr)
		}
	})
	t.Run("bad provider", func(t *testing.T) {
		ex, hub := closeExec(t, &fakeProcs{})
		r, _ := runClose(t, ex, hub, SessionClose{Provider: "bash", Cwd: "/w"})
		if r.Status != "failed" {
			t.Fatalf("%+v", r)
		}
	})
}

func TestCliOf(t *testing.T) {
	for args, want := range map[string]string{
		"/opt/homebrew/bin/claude":                                                                            "claude",
		"/Users/me/.local/share/claude/versions/2.1.296":                                                      "claude",
		"node /opt/homebrew/lib/node_modules/@anthropic-ai/claude-code/cli.js":                                "claude",
		"/opt/homebrew/Caskroom/codex/0.159.3/bin/codex":                                                      "codex",
		"node /opt/homebrew/bin/gemini":                                                                       "gemini",
		"/bin/zsh -c claude --resume":                                                                         "",
		"/opt/homebrew/Caskroom/claude-code@latest/2.1.295/claude --chrome-native-host":                       "",
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex app-server": "",
		"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex-code-mode-host":                     "",
		"node /srv/app.js": "",
		"":                 "",
	} {
		if got := cliOf(args); got != want {
			t.Errorf("%q: got %q want %q", args, got, want)
		}
	}
}

// The hub's side: refusals, the queue, the report marking the session closed.
func TestCloseSessionQueueAndReport(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.st.DB.Exec(`INSERT INTO sessions (id, host, path, provider, session_uuid, cwd, file_mtime, originator, agent_name) VALUES
		(1, 'studio', '/a.jsonl', 'claude', ?, '/w', ?, '', ''),
		(2, 'studio', '/b.jsonl', 'claude', 'u2', '/w', ?, 'sdk-cli', ''),
		(3, 'studio', '/c.jsonl', 'codex', 'u3', '/w', ?, '', 'fleet-17-fix'),
		(4, 'laptop', '/d.jsonl', 'codex', 'u4', '/w', ?, '', '')`, testUUID, store.Now(), store.Now(), store.Now(), store.Now()); err != nil {
		t.Fatal(err)
	}
	sess := func(id int64) store.Session {
		x, err := r.st.Session(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	for id, want := range map[int64]string{2: "headless", 3: "fleet run", 4: "no session agent"} {
		if _, err := r.ctl.CloseSession(ctx, sess(id)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("session %d: %v, want %q", id, err, want)
		}
	}
	id, err := r.ctl.CloseSession(ctx, sess(1))
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := r.ctl.CloseSession(ctx, sess(1)); again != id {
		t.Fatalf("a second Close queued another command: %d vs %d", again, id)
	}
	if x := sess(1); x.CloseCmd != id || x.CloseStatus != "queued" || x.Closed() {
		t.Fatalf("queued: %+v", x)
	}
	old := closeWait
	closeWait = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond}
	t.Cleanup(func() { closeWait = old })
	pt := &fakeProcs{procs: []Proc{{PID: 10, PPID: 1, Args: "claude"}}, cwd: map[int]string{10: "/w"}}
	t.Setenv("HOME", t.TempDir()) // no pid files: found by its folder
	r.ex.Procs = pt
	r.cycle(t)
	x := sess(1)
	if !x.Closed() || x.Live || x.CloseStatus != "done" {
		t.Fatalf("after close: %+v", x)
	}
	c, _ := r.st.Command(ctx, id)
	res, e := ParseCloseResult(c.Result)
	if e != "" || res.How != "cwd" || res.PIDs[0] != 10 {
		t.Fatalf("result %+v %q", res, e)
	}
	live, _ := r.st.LiveSessions(ctx)
	for _, l := range live {
		if l.ID == 1 {
			t.Fatalf("closed session still listed live")
		}
	}
	if len(live) != 3 {
		t.Fatalf("the other sessions should stay live: %d", len(live))
	}
	// Written again after the close (resumed): live again.
	if _, err := r.st.DB.Exec(`UPDATE sessions SET file_mtime = ? WHERE id = 1`, store.T(time.Now().Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if x := sess(1); x.Closed() || !x.Live {
		t.Fatalf("resumed session still closed: %+v", x)
	}
}
