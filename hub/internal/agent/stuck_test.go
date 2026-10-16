package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func sampleFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sample", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBlockedIn(t *testing.T) {
	for name, want := range map[string]string{
		"fifo-open.txt":  "open",   // a real sample: cat blocked opening a FIFO, the same kernel wait as a privacy prompt
		"claude-tcc.txt": "openat", // Claude Code's main thread on the Studio (other threads idle in kevent/psynch)
		"idle-sleep.txt": "",       // a real sample: sleep, idle in __semwait_signal
		"busy-scan.txt":  "",       // a walk that opens and stats a lot but never sits in one call
	} {
		fn, ok := blockedIn(sampleFixture(t, name))
		if fn != want || ok != (want != "") {
			t.Errorf("%s: got %q %v, want %q", name, fn, ok, want)
		}
	}
	if _, ok := blockedIn("no report at all"); ok {
		t.Error("garbage counted as blocked")
	}
}

func TestPSTimeAndLsof(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"0:01.23":     1230 * time.Millisecond,
		"12:00.50":    12*time.Minute + 500*time.Millisecond,
		"1:02:03.00":  time.Hour + 2*time.Minute + 3*time.Second,
		"2-01:00:00":  49 * time.Hour,
		"0:00.00":     0,
		"nonsense":    -1,
		"1:xx:00.00":  -1,
		"1:2:3:4.000": -1,
	} {
		got, ok := psTime(in)
		if (want < 0) == ok || (ok && got != want) {
			t.Errorf("%q: got %v %v, want %v", in, got, ok, want)
		}
	}
	ps := parsePSTime("  101     1   0:01.50 /bin/zsh -lic claude --resume x\n  102   101   1:00.00 claude --resume x\nbad line\n")
	if len(ps) != 2 || ps[1].PPID != 101 || ps[1].CPU != time.Minute || ps[0].Args != "/bin/zsh -lic claude --resume x" {
		t.Fatalf("ps %+v", ps)
	}
	dirs := parseLsofDirs("p49414\nfcwd\ntDIR\nn/Users/me/play\nftxt\ntREG\nn/bin/bfs\nf22\ntDIR\nn/Users/me/.claude\nf27\ntDIR\nn/Users/me/Documents\nf5\ntREG\nn/x\n")
	if strings.Join(dirs, ",") != "/Users/me/.claude,/Users/me/Documents" {
		t.Fatalf("dirs %v", dirs)
	}
}

// fakeStuck is a terminal's pane, processes and their stacks, set by the test.
type fakeStuck struct {
	mu      sync.Mutex
	panes   map[int64]Pane
	table   []ProcCPU
	samples map[int]string // pid → report
	sampled []int
	dirs    map[int][]string
}

func (f *fakeStuck) Panes(context.Context) (map[int64]Pane, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int64]Pane{}
	for k, v := range f.panes {
		out[k] = v
	}
	return out, nil
}
func (f *fakeStuck) Table(context.Context) ([]ProcCPU, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ProcCPU(nil), f.table...), nil
}
func (f *fakeStuck) Sample(_ context.Context, pid int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sampled = append(f.sampled, pid)
	return f.samples[pid], nil
}
func (f *fakeStuck) OpenDirs(_ context.Context, pid int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dirs[pid]
}
func (f *fakeStuck) setCPU(pid int, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.table {
		if f.table[i].PID == pid {
			f.table[i].CPU = d
		}
	}
}
func (f *fakeStuck) setScreen(id int64, h uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.panes[id]
	p.Screen = h
	f.panes[id] = p
}
func (f *fakeStuck) samplesOf() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.sampled...)
}

func TestWatchdogFindsAFrozenTerminal(t *testing.T) {
	tcc := sampleFixture(t, "claude-tcc.txt")
	idle := sampleFixture(t, "idle-sleep.txt")
	f := &fakeStuck{
		// 18: Claude frozen on a prompt, with its bfs walk under a shell. 19: Claude idle at its prompt. 20: busy.
		panes: map[int64]Pane{18: {PID: 100, Screen: 1}, 19: {PID: 200, Screen: 7}, 20: {PID: 300, Screen: 9}},
		table: []ProcCPU{
			{PID: 100, PPID: 1, CPU: 10 * time.Millisecond, Args: "/bin/zsh -lic claude --dangerously-skip-permissions --session-id 49713123-0779-46b0-a028-d1bae215b9d3 -- 'Anshuman wants a new part of Learn (this repo, biswas-dev/learn): personal courses'"},
			{PID: 101, PPID: 100, CPU: 30 * time.Second, Args: "claude --dangerously-skip-permissions --session-id 49713123-0779-46b0-a028-d1bae215b9d3 -- Anshuman wants a new part of Learn (this repo, biswas-dev/learn): personal courses on any topic"},
			{PID: 102, PPID: 101, CPU: 0, Args: "/bin/zsh -c source ~/.claude/shell-snapshots/x.sh && eval 'find ~ -maxdepth 3'"},
			{PID: 103, PPID: 102, CPU: 2 * time.Second, Args: "bfs -S dfs /Users/me -maxdepth 3 -type d"},
			{PID: 200, PPID: 1, CPU: 0, Args: "-zsh"},
			{PID: 201, PPID: 200, CPU: 5 * time.Second, Args: "claude"},
			{PID: 300, PPID: 1, CPU: 0, Args: "zsh"},
			{PID: 301, PPID: 300, CPU: time.Second, Args: "codex"},
			{PID: 999, PPID: 1, CPU: time.Hour, Args: "something else entirely"},
		},
		samples: map[int]string{101: tcc, 103: tcc, 201: idle},
		dirs:    map[int][]string{101: {"/Users/me/play/learn"}},
	}
	ex := &Executor{Host: "studio", Stuck: f, StateDir: t.TempDir()}
	ex.init()
	ctx := context.Background()
	seen := func() []store.TermSeen { return []store.TermSeen{{ID: 18}, {ID: 19}, {ID: 20}} }
	t0 := time.Date(2026, 10, 15, 22, 47, 0, 0, time.UTC)
	tick := func(at time.Duration) []store.TermSeen {
		s := seen()
		ex.watchTerms(ctx, s, t0.Add(at))
		ex.stuckWG.Wait()
		return s
	}
	busy := time.Duration(0)
	busyTick := func(at time.Duration) []store.TermSeen {
		busy += 5 * time.Second // terminal 20 keeps working
		f.setCPU(301, busy)
		return tick(at)
	}

	busyTick(0)
	if s := busyTick(2 * time.Minute); s[0].Blocked != nil || len(f.samplesOf()) != 0 {
		t.Fatalf("sampled before the stall was long enough: %v %+v", f.samplesOf(), s[0].Blocked)
	}
	// Three minutes still: sampled once, in the background; the finding lands in the next inventory.
	busyTick(3 * time.Minute)
	got := f.samplesOf()
	sort.Ints(got)
	if len(got) != 2 || got[0] != 101 || got[1] != 201 {
		// 18 is sampled at its CLI (101, not the shell under it) and stops there, blocked; 19's CLI (201) is
		// idle; 20 is busy and never sampled.
		t.Fatalf("samples %v", got)
	}
	s := busyTick(4 * time.Minute)
	b := s[0].Blocked
	if b == nil || b.Reason != "macOS privacy prompt" || b.PID != 101 || b.Func != "openat" || b.Path != "/Users/me/play/learn" ||
		b.Since != store.T(t0) || !strings.HasPrefix(b.Proc, "claude --dangerously-skip-permissions") || len([]rune(b.Proc)) > 80 {
		t.Fatalf("block %+v", b)
	}
	if len(b.FDA) == 0 {
		t.Errorf("no Full Disk Access paths: %+v", b)
	}
	if s[1].Blocked != nil || s[2].Blocked != nil {
		t.Fatalf("idle or busy terminals blocked: %+v %+v", s[1].Blocked, s[2].Blocked)
	}
	// Still frozen: still reported, never sampled again (nor is the idle one).
	n := len(f.samplesOf())
	if s := busyTick(10 * time.Minute); s[0].Blocked == nil || len(f.samplesOf()) != n {
		t.Fatalf("resampled or dropped: %v", f.samplesOf())
	}
	// Someone clicked Allow: it draws again, and the block clears at once.
	f.setScreen(18, 2)
	if s := busyTick(11 * time.Minute); s[0].Blocked != nil {
		t.Fatalf("still blocked after the screen changed: %+v", s[0].Blocked)
	}
	// CPU time moving (a slow turn with a still screen) is not a stall either.
	f.setCPU(101, time.Minute)
	busyTick(12 * time.Minute)
	f.setCPU(101, 2*time.Minute)
	if s := busyTick(16 * time.Minute); s[0].Blocked != nil || len(f.samplesOf()) != n {
		t.Fatalf("a terminal using CPU was sampled: %v", f.samplesOf())
	}
	// A terminal that ended is forgotten.
	ex.watchTerms(ctx, []store.TermSeen{{ID: 19}}, t0.Add(17*time.Minute))
	ex.mu.Lock()
	_, kept := ex.stalls[18]
	ex.mu.Unlock()
	if kept {
		t.Fatal("an ended terminal's watch was kept")
	}
}

func TestTermScriptResume(t *testing.T) {
	id := "49713123-0779-46b0-a028-d1bae215b9d3"
	s, _, err := TermScript(TermOpen{Provider: "claude", Resume: id, SessionID: "11111111-2222-3333-4444-555555555555"}, Gateway{}, "/bin/zsh")
	if err != nil || s != "claude --dangerously-skip-permissions --resume "+id+"; exec /bin/zsh -l" {
		t.Fatalf("claude: %s %v", s, err)
	}
	s, _, err = TermScript(TermOpen{Provider: "codex", Resume: id, Ask: true}, Gateway{}, "/bin/zsh")
	if err != nil || s != "codex resume --ask-for-approval on-request --sandbox workspace-write "+id+"; exec /bin/zsh -l" {
		t.Fatalf("codex: %s %v", s, err)
	}
	if _, _, err := TermScript(TermOpen{Provider: "claude", Resume: "x; rm -rf ~"}, Gateway{}, "/bin/zsh"); err == nil {
		t.Fatal("a bad resume id was accepted")
	}
}

func TestBlockedTerminalSyncAndRestart(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	open := func(provider string) int64 {
		id, err := r.ctl.OpenTerminal(ctx, TermReq{Host: "studio", Provider: provider, Cwd: "~/play/learn", Ask: true, Model: "opus"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	cl, cx, sh := open("claude"), open("codex"), open("shell")
	inv := func(ts ...store.TermSeen) {
		t.Helper()
		if err := r.hub.Inventory(ctx, Inventory{Host: "studio", Tmux: true, TermsAt: time.Now().Add(time.Minute), Terminals: ts}); err != nil {
			t.Fatal(err)
		}
	}
	blk := &store.TermBlock{Reason: "macOS privacy prompt", PID: 101, Proc: "claude", Func: "openat", Since: store.T(time.Now())}
	inv(store.TermSeen{ID: cl, Blocked: blk}, store.TermSeen{ID: cx}, store.TermSeen{ID: sh})
	term, _ := r.st.Terminal(ctx, cl)
	if b := term.Block(); term.Status != "live" || b == nil || b.Func != "openat" {
		t.Fatalf("not blocked: %+v", term)
	}
	if other, _ := r.st.Terminal(ctx, cx); other.Block() != nil {
		t.Fatal("the codex terminal is blocked too")
	}

	// Restart: the flags it was opened with, continuing the conversation; a shell has nothing to restart.
	if _, err := r.ctl.RestartTerminal(ctx, sh, ""); err == nil {
		t.Fatal("restarted a shell")
	}
	if _, err := r.ctl.RestartTerminal(ctx, cl, "not-a-uuid"); err == nil {
		t.Fatal("bad resume id accepted")
	}
	resume := "49713123-0779-46b0-a028-d1bae215b9d3"
	cid, err := r.ctl.RestartTerminal(ctx, cl, resume)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.st.Command(ctx, cid)
	var o TermOpen
	_ = json.Unmarshal(c.Payload, &o)
	if c.Kind != KindTermRestart || c.AgentID != cl || o.Resume != resume || o.SessionID != "" || !o.Ask || o.Model != "opus" || o.Prompt != "" {
		t.Fatalf("restart command %+v %+v", c, o)
	}
	// Nothing to resume yet: start again under the same Claude session id, so the hub still knows it.
	cid2, _ := r.ctl.RestartTerminal(ctx, cl, "")
	c2, _ := r.st.Command(ctx, cid2)
	o = TermOpen{}
	_ = json.Unmarshal(c2.Payload, &o)
	if o.Resume != "" || o.SessionID != term.SessionUUID || o.SessionID == "" {
		t.Fatalf("fresh restart %+v", o)
	}
	// The restart worked: blocked clears now, without waiting for the next inventory.
	if err := r.hub.Report(ctx, cid, Report{Status: "done"}); err != nil {
		t.Fatal(err)
	}
	if term, _ := r.st.Terminal(ctx, cl); term.Blocked != "" || term.Status != "live" {
		t.Fatalf("after restart %+v", term)
	}
	// A restart of a terminal whose tmux is gone ends it.
	if err := r.hub.Report(ctx, cid2, Report{Status: "failed", Error: ErrTermEnded}); err != nil {
		t.Fatal(err)
	}
	if term, _ := r.st.Terminal(ctx, cl); term.Status != "ended" {
		t.Fatalf("status %s", term.Status)
	}
	// An inventory without the block clears it; one without the terminal ends it and clears it.
	inv(store.TermSeen{ID: cx, Blocked: blk}, store.TermSeen{ID: sh})
	if x, _ := r.st.Terminal(ctx, cx); x.Block() == nil {
		t.Fatal("codex not blocked")
	}
	inv(store.TermSeen{ID: cx}, store.TermSeen{ID: sh})
	if x, _ := r.st.Terminal(ctx, cx); x.Blocked != "" {
		t.Fatal("block kept after the agent stopped reporting it")
	}
}

// fakeTree is a process table for endTree: TERM ends some, KILL the rest.
type fakeTree struct {
	mu      sync.Mutex
	alive   map[int]bool
	ignores map[int]bool // TERM does nothing (a process held in a privacy prompt)
	sigs    []string
}

func (f *fakeTree) List(context.Context) ([]Proc, error)   { return nil, nil }
func (f *fakeTree) OpenedBy(context.Context, string) []int { return nil }
func (f *fakeTree) Cwd(context.Context, int) string        { return "" }
func (f *fakeTree) Alive(pid int) bool                     { f.mu.Lock(); defer f.mu.Unlock(); return f.alive[pid] }
func (f *fakeTree) Signal(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sigs = append(f.sigs, sig.String()+":"+strconv.Itoa(pid))
	if sig == syscall.SIGKILL || !f.ignores[pid] {
		f.alive[pid] = false
	}
	return nil
}

func TestEndTreeEscalates(t *testing.T) {
	f := &fakeTree{alive: map[int]bool{1: true, 2: true}, ignores: map[int]bool{2: true}}
	ex := &Executor{Procs: f}
	start := time.Now()
	ex.endTree(context.Background(), []int{1, 2})
	if strings.Join(f.sigs, " ") != "terminated:1 terminated:2 killed:2" || f.alive[1] || f.alive[2] {
		t.Fatalf("signals %v alive %v", f.sigs, f.alive)
	}
	if time.Since(start) > 8*time.Second {
		t.Fatal("waited too long")
	}
}
