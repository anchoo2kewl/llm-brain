package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// The stuck-terminal watchdog. A CLI in a hub terminal can freeze waiting for something only the Mac's own
// screen can answer: a macOS privacy prompt (TCC: "allow access to Documents?") blocks the open() that
// triggered it until someone clicks, and nobody is at the Mac. The CLI then reads no keys, draws nothing and
// uses no CPU — it looks like a dead terminal. The watchdog spots it cheaply and the hub says what to do.
//
// Every inventory (once a minute) it notes each terminal's screen (a hash of capture-pane) and the CPU time
// of the processes in its pane. A terminal whose screen and CPU time both stood still for stallFor is a
// suspect; only then, once per stall, the agent samples its processes (macOS `sample`, 1 s). A process
// whose thread sat in a file-system call (open, stat, getattrlist…) for the whole sample is blocked. Any
// change on screen or in CPU time ends the stall and clears it.

const (
	stallFor   = 3 * time.Minute // still this long before a terminal is sampled
	stallCPU   = time.Second     // CPU time this small over a stall counts as none (timers, GC)
	sampleMax  = 4               // processes sampled per terminal
	blockShare = 0.9             // a thread this much of the sample in one call is sitting in it
)

// StuckProbe is what the watchdog reads. RealStuck uses tmux, ps, sample and lsof; tests use a fake.
type StuckProbe interface {
	Panes(ctx context.Context) (map[int64]Pane, error) // by terminal id
	Table(ctx context.Context) ([]ProcCPU, error)
	Sample(ctx context.Context, pid int) (string, error) // macOS sample's report
	OpenDirs(ctx context.Context, pid int) []string      // folders it has open, by file descriptor
}

// Pane is a terminal's tmux pane: the process tmux started and a hash of what's on screen.
type Pane struct {
	PID    int
	Screen uint64
}

// ProcCPU is a process with its CPU time so far.
type ProcCPU struct {
	PID, PPID int
	CPU       time.Duration
	Args      string
}

// stall is one terminal's watch: what was on screen, its CPU time, and since when both stood still.
type stall struct {
	screen   uint64
	cpu      time.Duration
	since    time.Time
	sampling bool             // a sample is running
	sampled  bool             // this stall was sampled already
	block    *store.TermBlock // the finding, reported until the stall ends
}

// stuckMax: how long a sample of one terminal may take, all processes together.
const stuckMax = 20 * time.Second

func (e *Executor) stuckProbe() StuckProbe {
	if e.Stuck != nil {
		return e.Stuck
	}
	t, err := e.tmux()
	if err != nil {
		return nil
	}
	return RealStuck{T: t}
}

// watchTerms updates each terminal's stall and sets Blocked on the ones found frozen. It returns at once:
// samples run in the background and land in the next inventory.
func (e *Executor) watchTerms(ctx context.Context, seen []store.TermSeen, now time.Time) {
	p := e.stuckProbe()
	if p == nil || len(seen) == 0 {
		e.mu.Lock()
		e.stalls = nil
		e.mu.Unlock()
		return
	}
	panes, err := p.Panes(ctx)
	if err != nil {
		return
	}
	table, err := p.Table(ctx)
	if err != nil {
		return
	}
	kids := map[int][]int{}
	byPID := map[int]ProcCPU{}
	for _, x := range table {
		kids[x.PPID] = append(kids[x.PPID], x.PID)
		byPID[x.PID] = x
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stalls == nil {
		e.stalls = map[int64]*stall{}
	}
	live := map[int64]bool{}
	for i := range seen {
		id := seen[i].ID
		live[id] = true
		pane, ok := panes[id]
		if !ok {
			continue
		}
		tree := descendants(pane.PID, kids)
		var cpu time.Duration
		for _, pid := range append([]int{pane.PID}, tree...) {
			cpu += byPID[pid].CPU
		}
		st := e.stalls[id]
		if st == nil || st.screen != pane.Screen || cpu < st.cpu || cpu-st.cpu >= stallCPU {
			if st != nil && st.block != nil {
				e.logf("agent: terminal %d: moving again", id)
			}
			e.stalls[id] = &stall{screen: pane.Screen, cpu: cpu, since: now}
			continue
		}
		if st.block != nil {
			b := *st.block
			seen[i].Blocked = &b
			continue
		}
		if st.sampling || st.sampled || now.Sub(st.since) < stallFor {
			continue
		}
		var cands []ProcCPU
		for _, pid := range tree {
			if x := byPID[pid]; !isShell(x.Args) && len(cands) < sampleMax {
				cands = append(cands, x)
			}
		}
		if len(cands) == 0 {
			st.sampled = true
			continue
		}
		st.sampling = true
		e.stuckWG.Add(1)
		go func(id int64, st *stall, cands []ProcCPU) {
			defer e.stuckWG.Done()
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stuckMax)
			defer cancel()
			b := e.findBlocked(sctx, p, cands)
			e.mu.Lock()
			defer e.mu.Unlock()
			st.sampling, st.sampled = false, true
			if b != nil && e.stalls[id] == st { // the stall is still the same one
				b.Since = store.T(st.since)
				st.block = b
				e.logf("agent: terminal %d: frozen in %s (%s): %s", id, b.Func, b.Proc, b.Reason)
			}
		}(id, st, cands)
	}
	for id := range e.stalls {
		if !live[id] {
			delete(e.stalls, id)
		}
	}
}

// findBlocked samples each candidate and returns the first one sitting in a file-system call.
func (e *Executor) findBlocked(ctx context.Context, p StuckProbe, cands []ProcCPU) *store.TermBlock {
	for _, x := range cands {
		out, err := p.Sample(ctx, x.PID)
		if err != nil {
			continue
		}
		fn, ok := blockedIn(out)
		if !ok {
			continue
		}
		b := &store.TermBlock{Reason: "macOS privacy prompt", PID: x.PID, Proc: shortArgs(x.Args), Func: fn, FDA: e.fdaPaths()}
		if dirs := p.OpenDirs(ctx, x.PID); len(dirs) > 0 {
			b.Path = dirs[len(dirs)-1] // the newest folder it opened: where the walk had got to
		}
		return b
	}
	return nil
}

// fdaPaths are the binaries to give Full Disk Access so terminals stop asking: this agent and tmux.
func (e *Executor) fdaPaths() []string {
	var out []string
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		out = append(out, exe)
	}
	if t, err := e.tmux(); err == nil && t.bin != "" {
		bin := t.bin
		if r, err := filepath.EvalSymlinks(bin); err == nil {
			bin = r
		}
		out = append(out, bin)
	}
	return out
}

// descendants lists every process under pid, nearest first.
func descendants(pid int, kids map[int][]int) []int {
	var out []int
	q := append([]int(nil), kids[pid]...)
	seen := map[int]bool{pid: true}
	for len(q) > 0 {
		x := q[0]
		q = q[1:]
		if seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
		q = append(q, kids[x]...)
	}
	return out
}

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "tcsh": true, "csh": true}

// isShell: a shell in the tree waits on its children; sampling it says nothing.
func isShell(args string) bool {
	f := strings.Fields(args)
	if len(f) == 0 {
		return true
	}
	return shells[strings.TrimPrefix(filepath.Base(f[0]), "-")]
}

func shortArgs(a string) string {
	a = strings.Join(strings.Fields(a), " ")
	if r := []rune(a); len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return a
}

// The calls a privacy prompt holds a process in: opening, stat-ing or listing a protected folder.
var blockingCalls = map[string]bool{
	"open": true, "__open": true, "openat": true, "__openat": true, "__open_nocancel": true, "__openat_nocancel": true,
	"stat": true, "stat64": true, "__stat64": true, "lstat": true, "lstat64": true, "fstatat": true, "fstatat64": true, "__fstatat64": true,
	"getattrlist": true, "__getattrlist": true, "getattrlistat": true, "getattrlistbulk": true, "__getattrlistbulk": true,
	"getdirentries": true, "__getdirentries64": true, "access": true, "__access": true, "faccessat": true, "__faccessat": true,
	"opendir": true, "__opendir2": true,
}

var (
	sampleThread = regexp.MustCompile(`^ {4}(\d+) +Thread_`)
	sampleFrame  = regexp.MustCompile(`^[ +!:|]*?(\d+) +(\S+)`)
)

// blockedIn reads a macOS sample report: a thread that spent blockShare of its samples in one
// file-system call is sitting in it. It returns the call.
func blockedIn(report string) (string, bool) {
	sc := bufio.NewScanner(strings.NewReader(report))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	in, total := false, 0
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "Call graph:"):
			in = true
			continue
		case strings.HasPrefix(l, "Total number in stack"), strings.HasPrefix(l, "Sort by top of stack"), strings.HasPrefix(l, "Binary Images"):
			in = false
		}
		if !in {
			continue
		}
		if m := sampleThread.FindStringSubmatch(l); m != nil {
			total, _ = strconv.Atoi(m[1])
			continue
		}
		m := sampleFrame.FindStringSubmatch(l)
		if m == nil || total < 10 {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		fn, _, _ := strings.Cut(m[2], "$") // openat$NOCANCEL
		if blockingCalls[fn] && float64(n) >= blockShare*float64(total) {
			return strings.TrimLeft(fn, "_"), true
		}
	}
	return "", false
}

// ---------------------------------------------------------------- restart

// termRestart ends a terminal's CLI (and everything under it) and starts it again in the same tmux pane,
// continuing the same conversation when the hub knows it. The pane gets a fresh pseudo-terminal, so keys
// typed into the frozen one are dropped instead of reaching the new shell.
func (e *Executor) termRestart(ctx context.Context, c store.Command) {
	l := e.termLock(c.AgentID)
	l.Lock()
	defer l.Unlock()
	var o TermOpen
	if err := json.Unmarshal(c.Payload, &o); err != nil || o.Provider == "shell" || !KnownTermProvider(o.Provider) {
		e.report(ctx, c.ID, Report{Status: "failed", Error: "bad payload"})
		return
	}
	fail := func(err error) {
		e.logf("agent: terminal %d: restart: %v", c.AgentID, err)
		e.report(ctx, c.ID, Report{Status: "failed", Error: err.Error()})
	}
	t, err := e.tmux()
	if err != nil {
		fail(err)
		return
	}
	name := "=" + TmuxName(c.AgentID) + ":"
	out, err := t.run(ctx, "display-message", "-p", "-t", name, "#{pane_pid}\t#{session_path}")
	if err != nil {
		fail(errors.New(ErrTermEnded))
		return
	}
	pidStr, cwd, _ := strings.Cut(strings.TrimSpace(out), "\t")
	panePID, err := strconv.Atoi(pidStr)
	if err != nil {
		fail(fmt.Errorf("tmux pane: %q", out))
		return
	}
	// End the CLI and what it started. A process held by a privacy prompt may ignore TERM; KILL ends it.
	if p := e.stuckProbe(); p != nil {
		if table, err := p.Table(ctx); err == nil {
			kids := map[int][]int{}
			for _, x := range table {
				kids[x.PPID] = append(kids[x.PPID], x.PID)
			}
			e.endTree(ctx, descendants(panePID, kids))
		}
	}
	gw := Gateway{}
	if e.Config.TerminalGateway && (o.Provider == "claude" || o.Provider == "codex") {
		gw = e.Config.ResolveGateway(ctx)
	}
	o.Prompt, o.Worktree = "", nil
	shell := userShell()
	script, env, err := TermScript(o, gw, shell)
	if err != nil {
		fail(err)
		return
	}
	args := []string{"respawn-pane", "-k", "-t", name}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	env = append(env, "LLM_BRAIN_REMOTE=1", "LLM_BRAIN_TERMINAL="+strconv.FormatInt(c.AgentID, 10))
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	args = append(args, shell, "-lic", script)
	if _, err := t.run(context.WithoutCancel(ctx), args...); err != nil {
		fail(err)
		return
	}
	e.mu.Lock()
	delete(e.stalls, c.AgentID)
	e.mu.Unlock()
	go e.acceptTrust(context.WithoutCancel(ctx), t, c.AgentID)
	how := "fresh"
	if o.Resume != "" {
		how = "resumed " + o.Resume
	}
	e.logf("agent: terminal %d: restarted %s (%s)", c.AgentID, o.Provider, how)
	e.report(ctx, c.ID, Report{Status: "done", Cwd: cwd})
}

// endTree sends TERM to pids, then KILL to whatever is left after a few seconds.
func (e *Executor) endTree(ctx context.Context, pids []int) {
	if len(pids) == 0 {
		return
	}
	pt := e.procs()
	for _, pid := range pids {
		_ = pt.Signal(pid, syscall.SIGTERM)
	}
	if waitGone(ctx, pt, pids, 3*time.Second) {
		return
	}
	for _, pid := range pids {
		if pt.Alive(pid) {
			_ = pt.Signal(pid, syscall.SIGKILL)
		}
	}
	waitGone(ctx, pt, pids, 2*time.Second)
}

// ---------------------------------------------------------------- the real probe

// RealStuck reads this Mac: tmux for panes, ps for CPU time, sample for stacks, lsof for open folders.
type RealStuck struct{ T tmux }

func (r RealStuck) Panes(ctx context.Context) (map[int64]Pane, error) {
	out, err := r.T.run(ctx, "list-panes", "-a", "-F", "#{session_name} #{pane_pid}")
	if err != nil {
		return nil, err
	}
	panes := map[int64]Pane{}
	for _, l := range strings.Split(out, "\n") {
		name, pid, _ := strings.Cut(strings.TrimSpace(l), " ")
		s, ok := strings.CutPrefix(name, "llmb-")
		if !ok {
			continue
		}
		id, err1 := strconv.ParseInt(s, 10, 64)
		p, err2 := strconv.Atoi(pid)
		if err1 != nil || err2 != nil {
			continue
		}
		if _, dup := panes[id]; dup { // one pane per terminal; a second (split) changes nothing here
			continue
		}
		screen, err := r.T.run(ctx, "capture-pane", "-p", "-J", "-t", "="+name+":")
		if err != nil {
			continue
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(screen))
		panes[id] = Pane{PID: p, Screen: h.Sum64()}
	}
	return panes, nil
}

func (RealStuck) Table(ctx context.Context) ([]ProcCPU, error) {
	cmd := exec.CommandContext(ctx, "ps", "-axww", "-o", "pid=,ppid=,time=,args=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parsePSTime(string(out)), nil
}

// parsePSTime reads `ps -o pid=,ppid=,time=,args=`.
func parsePSTime(out string) []ProcCPU {
	var ps []ProcCPU
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 4 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		cpu, ok := psTime(f[2])
		if e1 != nil || e2 != nil || !ok {
			continue
		}
		ps = append(ps, ProcCPU{PID: pid, PPID: ppid, CPU: cpu, Args: strings.Join(f[3:], " ")})
	}
	return ps
}

// psTime parses ps's cumulative CPU time: [[dd-]hh:]mm:ss[.cc].
func psTime(s string) (time.Duration, bool) {
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	sec, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil {
		return 0, false
	}
	total := time.Duration(sec * float64(time.Second))
	unit := time.Minute
	for i := len(parts) - 2; i >= 0; i-- {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return 0, false
		}
		total += time.Duration(n) * unit
		unit *= 60
	}
	return total + time.Duration(days)*24*time.Hour, true
}

func (RealStuck) Sample(ctx context.Context, pid int) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("sample is macOS only")
	}
	out, err := exec.CommandContext(ctx, "/usr/bin/sample", strconv.Itoa(pid), "1", "-mayDie").Output()
	return string(out), err
}

func (RealStuck) OpenDirs(ctx context.Context, pid int) []string {
	out, _ := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-Fftn").Output()
	return parseLsofDirs(string(out))
}

// parseLsofDirs reads lsof -Fftn: the folders open on numbered descriptors (not cwd or txt), by descriptor.
func parseLsofDirs(out string) []string {
	type ent struct {
		fd   int
		name string
	}
	var ents []ent
	fd, typ := -1, ""
	for _, l := range strings.Split(out, "\n") {
		if l == "" {
			continue
		}
		switch l[0] {
		case 'f':
			n, err := strconv.Atoi(l[1:])
			if err != nil {
				n = -1
			}
			fd, typ = n, ""
		case 't':
			typ = l[1:]
		case 'n':
			if fd >= 0 && typ == "DIR" {
				ents = append(ents, ent{fd, l[1:]})
			}
		}
	}
	sort.SliceStable(ents, func(i, j int) bool { return ents[i].fd < ents[j].fd })
	var dirs []string
	for _, x := range ents {
		dirs = append(dirs, x.name)
	}
	return dirs
}
