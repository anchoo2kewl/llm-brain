package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Closing a session: ending the process of a Claude Code, Codex or Gemini CLI that runs on some Mac, started
// by the hub or not. A hub terminal is ended as a terminal (term_kill); everything else is a session_close
// for the Mac it runs on, which finds the process and signals it: TERM, then HUP, then KILL.
//
// Finding the process, most certain first:
//  1. Claude records each running CLI in ~/.claude/sessions/<pid>.json with its session id and the process's
//     start time (which guards against a reused pid).
//  2. The process that holds the transcript open (lsof): Codex keeps its rollout file open.
//  3. The one process of that CLI whose working directory is the session's folder; two or more is
//     ambiguous and nothing is signalled.
//
// Only claude/codex/gemini processes are ever signalled, never a headless one (claude -p, codex exec: fleet
// runs and hub tasks have their own stop), and never the hub or this agent or anything they run inside.

const KindSessionClose = store.KindSessionClose

// SessionClose is a session_close payload.
type SessionClose struct {
	SessionID int64  `json:"session_id"`
	Provider  string `json:"provider"`
	UUID      string `json:"uuid"`
	Path      string `json:"path"` // the transcript
	Cwd       string `json:"cwd"`
}

// CloseResult is a session_close's result: what was signalled, found how, and the last signal it took.
type CloseResult struct {
	PIDs   []int  `json:"pids,omitempty"`
	How    string `json:"how,omitempty"`    // pidfile | open-file | cwd
	Signal string `json:"signal,omitempty"` // TERM | HUP | KILL
	Note   string `json:"note,omitempty"`   // "not running": nothing to end
}

// Proc is a process in the table.
type Proc struct {
	PID, PPID int
	Start     string // ps lstart, whitespace-normalised: "Thu Oct 15 17:52:32 2026"
	Args      string
}

// Procs is the process table. RealProcs reads it with ps and lsof; tests use a fake.
type Procs interface {
	List(ctx context.Context) ([]Proc, error)
	OpenedBy(ctx context.Context, path string) []int // pids holding path open
	Cwd(ctx context.Context, pid int) string
	Signal(pid int, sig syscall.Signal) error
	Alive(pid int) bool
}

// closeWait is how long each signal gets before the next, stronger one.
var closeWait = []time.Duration{4 * time.Second, 2 * time.Second, time.Second}

// HeadlessOriginator: a transcript written by a non-interactive run (claude -p through the SDK, codex exec).
func HeadlessOriginator(originator string) bool {
	o := strings.ToLower(originator)
	return strings.HasPrefix(o, "sdk") || strings.Contains(o, "exec")
}

// CloseSession ends a session's CLI process on its Mac: it queues a session_close and returns its id (or the
// one already queued). The caller ends hub terminals itself (KillTerminal).
func (c *Control) CloseSession(ctx context.Context, x store.Session) (int64, error) {
	switch {
	case x.ParentUUID != "":
		return 0, bad("this is a subagent; close the session it belongs to")
	case x.RunID > 0 || strings.HasPrefix(x.AgentName, "fleet-"):
		return 0, bad("this is a fleet run; cancel it from its run page")
	case HeadlessOriginator(x.Originator):
		return 0, bad("this is a headless run; it ends by itself")
	case x.Provider != "claude" && x.Provider != "codex" && x.Provider != "gemini":
		return 0, bad("only Claude, Codex and Gemini sessions can be closed")
	}
	if x.CloseCmd > 0 {
		if cmd, err := c.S.Command(ctx, x.CloseCmd); err == nil && !store.FinalStatus(cmd.Status) {
			return cmd.ID, nil // already on its way
		}
	}
	if _, err := c.liveHost(ctx, x.Host); err != nil {
		return 0, err
	}
	b, _ := json.Marshal(SessionClose{SessionID: x.ID, Provider: x.Provider, UUID: x.UUID, Path: x.Path, Cwd: x.Cwd})
	id, err := c.S.QueueCommand(ctx, store.Command{Host: x.Host, Kind: KindSessionClose, Payload: b})
	if err != nil {
		return 0, err
	}
	return id, c.S.SetSessionClose(ctx, x.ID, id)
}

// sessionClosed applies a finished session_close: the session is closed unless it failed.
func (h *LocalHub) sessionClosed(ctx context.Context, c store.Command, r Report) error {
	if r.Status != "done" {
		return nil
	}
	var p SessionClose
	if err := json.Unmarshal(c.Payload, &p); err != nil || p.SessionID == 0 {
		return nil
	}
	return h.S.MarkSessionClosed(ctx, p.SessionID)
}

// ParseCloseResult reads a session_close's result (the Report JSON the hub stored).
func ParseCloseResult(body string) (CloseResult, string) {
	var r Report
	var cr CloseResult
	if json.Unmarshal([]byte(body), &r) != nil {
		return cr, ""
	}
	_ = json.Unmarshal([]byte(r.Result), &cr)
	return cr, r.Error
}

func (e *Executor) procs() Procs {
	if e.Procs != nil {
		return e.Procs
	}
	return RealProcs{}
}

// sessionClose finds a session's process and ends it.
func (e *Executor) sessionClose(ctx context.Context, c store.Command) {
	var p SessionClose
	if err := json.Unmarshal(c.Payload, &p); err != nil || (p.Provider != "claude" && p.Provider != "codex" && p.Provider != "gemini") {
		e.report(ctx, c.ID, Report{Status: "failed", Error: "bad payload"})
		return
	}
	e.report(ctx, c.ID, Report{Status: "running"})
	res, err := e.closeProcs(ctx, p)
	if err != nil {
		e.report(ctx, c.ID, Report{Status: "failed", Error: err.Error()})
		return
	}
	b, _ := json.Marshal(res)
	e.logf("agent: closed %s session %s: %s", p.Provider, p.UUID, b)
	e.report(ctx, c.ID, Report{Status: "done", Result: string(b)})
}

func (e *Executor) closeProcs(ctx context.Context, p SessionClose) (CloseResult, error) {
	pt := e.procs()
	all, err := pt.List(ctx)
	if err != nil {
		return CloseResult{}, fmt.Errorf("list processes: %w", err)
	}
	byPID := map[int]Proc{}
	for _, x := range all {
		byPID[x.PID] = x
	}
	mine := func(x Proc) bool { return cliOf(x.Args) == p.Provider }
	var cands []int
	how := ""
	// 1. Claude's own record of its running sessions.
	if p.Provider == "claude" && uuidRe.MatchString(p.UUID) {
		for _, pid := range claudePIDs(p.UUID) {
			if x, ok := byPID[pid.PID]; ok && mine(x) && (pid.Start == "" || normSpace(pid.Start) == x.Start) {
				if pid.Headless {
					return CloseResult{}, errors.New("that Claude session runs headless (claude -p); it ends by itself")
				}
				cands = append(cands, pid.PID)
			}
		}
		how = "pidfile"
	}
	// 2. Whoever holds the transcript open.
	if len(cands) == 0 && p.Path != "" {
		for _, pid := range pt.OpenedBy(ctx, p.Path) {
			if x, ok := byPID[pid]; ok && mine(x) {
				cands = append(cands, pid)
			}
		}
		how = "open-file"
	}
	// 3. The one process of that CLI in the session's folder.
	if len(cands) == 0 && p.Cwd != "" {
		want := map[string]bool{filepath.Clean(p.Cwd): true}
		if r, err := filepath.EvalSymlinks(p.Cwd); err == nil {
			want[r] = true
		}
		for _, x := range all {
			if mine(x) && want[filepath.Clean(pt.Cwd(ctx, x.PID))] {
				cands = append(cands, x.PID)
			}
		}
		how = "cwd"
	}
	groups := procGroups(cands, byPID)
	if len(groups) == 0 {
		return CloseResult{Note: "not running"}, nil
	}
	if len(groups) > 1 {
		var tops []string
		for _, g := range groups {
			tops = append(tops, strconv.Itoa(g[0])+" "+trimArgs(byPID[g[0]].Args))
		}
		return CloseResult{}, fmt.Errorf("ambiguous: %d %s processes in %s (%s); close it on the Mac", len(groups), p.Provider, p.Cwd,
			strings.Join(tops, "; "))
	}
	targets := groups[0]
	// Its own children of the same CLI too (a node wrapper's native binary).
	for _, x := range all {
		if mine(x) && x.PPID == targets[0] && !hasInt(targets, x.PID) {
			targets = append(targets, x.PID)
		}
	}
	protected := ancestors(os.Getpid(), byPID)
	for _, pid := range targets {
		x := byPID[pid]
		if pid <= 1 || protected[pid] {
			return CloseResult{}, fmt.Errorf("refusing to signal %d: the hub agent runs inside it", pid)
		}
		if !mine(x) {
			return CloseResult{}, fmt.Errorf("refusing to signal %d: not a %s process", pid, p.Provider)
		}
		if headlessArgs(p.Provider, x.Args) {
			return CloseResult{}, fmt.Errorf("refusing to signal %d: a headless %s run (fleet runs and hub tasks have their own stop)", pid, p.Provider)
		}
	}
	res := CloseResult{PIDs: targets, How: how}
	sigs := []struct {
		s    syscall.Signal
		name string
	}{{syscall.SIGTERM, "TERM"}, {syscall.SIGHUP, "HUP"}, {syscall.SIGKILL, "KILL"}}
	for i, sg := range sigs {
		res.Signal = sg.name
		for _, pid := range targets {
			if pt.Alive(pid) {
				_ = pt.Signal(pid, sg.s)
			}
		}
		if waitGone(ctx, pt, targets, closeWait[i]) {
			return res, nil
		}
	}
	return res, fmt.Errorf("pid %v still running after KILL", targets)
}

func waitGone(ctx context.Context, pt Procs, pids []int, d time.Duration) bool {
	end := time.Now().Add(d)
	for {
		alive := false
		for _, pid := range pids {
			alive = alive || pt.Alive(pid)
		}
		if !alive {
			return true
		}
		if time.Now().After(end) || ctx.Err() != nil {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// procGroups merges candidates that are a parent and child into one group, topmost first.
func procGroups(cands []int, byPID map[int]Proc) [][]int {
	in := map[int]bool{}
	for _, c := range cands {
		in[c] = true
	}
	top := func(pid int) int {
		t := pid
		for p, n := byPID[pid].PPID, 0; p > 1 && n < 64; p, n = byPID[p].PPID, n+1 {
			if in[p] {
				t = p
			}
		}
		return t
	}
	by := map[int][]int{}
	var order []int
	for _, c := range cands {
		t := top(c)
		if _, ok := by[t]; !ok {
			order = append(order, t)
			by[t] = []int{t}
		}
		if c != t && !hasInt(by[t], c) {
			by[t] = append(by[t], c)
		}
	}
	sort.Ints(order)
	var out [][]int
	for _, t := range order {
		out = append(out, by[t])
	}
	return out
}

// ancestors is pid and every process it runs inside.
func ancestors(pid int, byPID map[int]Proc) map[int]bool {
	out := map[int]bool{pid: true, os.Getppid(): true}
	for p, n := byPID[pid].PPID, 0; p > 1 && n < 64; p, n = byPID[p].PPID, n+1 {
		out[p] = true
	}
	return out
}

func hasInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func normSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func trimArgs(a string) string {
	if len(a) > 80 {
		return a[:80] + "…"
	}
	return a
}

// cliOf names the CLI a command line runs: claude, codex, gemini or "".
func cliOf(args string) string {
	f := strings.Fields(args)
	if len(f) == 0 {
		return ""
	}
	bin := f[0]
	base := filepath.Base(bin)
	if strings.Contains(args, ".app/Contents/") || strings.Contains(args, "--chrome-native-host") {
		return "" // a desktop app's own engine (ChatGPT's codex) or a browser helper, never a session
	}
	if base == "node" || base == "bun" { // an npm install: the script says which
		if len(f) < 2 {
			return ""
		}
		s := f[1]
		switch {
		case strings.Contains(s, "@anthropic-ai/claude-code") || filepath.Base(s) == "claude":
			return "claude"
		case strings.Contains(s, "@openai/codex") || filepath.Base(s) == "codex":
			return "codex"
		case strings.Contains(s, "gemini-cli") || filepath.Base(s) == "gemini":
			return "gemini"
		}
		return ""
	}
	switch {
	case base == "claude" || strings.Contains(bin, "/claude/versions/"):
		return "claude"
	case base == "codex" || strings.HasPrefix(base, "codex-aarch64") || strings.HasPrefix(base, "codex-x86_64"):
		return "codex"
	case base == "gemini":
		return "gemini"
	}
	return ""
}

// headlessArgs: a non-interactive run (claude -p, codex exec, gemini -p).
func headlessArgs(cli, args string) bool {
	f := strings.Fields(args)
	if len(f) > 0 && (filepath.Base(f[0]) == "node" || filepath.Base(f[0]) == "bun") {
		f = f[1:]
	}
	if len(f) > 0 {
		f = f[1:]
	}
	// ps joins argv with spaces, so a prompt's words are tokens too: a prompt that says "exec" makes an
	// interactive session look headless, which only ever refuses a close (the safe side).
	for _, a := range f {
		switch cli {
		case "claude", "gemini":
			if a == "-p" || a == "--print" || a == "--prompt" || strings.HasPrefix(a, "--print=") || strings.HasPrefix(a, "--prompt=") ||
				(cli == "claude" && a == "mcp") {
				return true
			}
		case "codex":
			switch a {
			case "exec", "e", "exec-server", "mcp", "mcp-server", "app-server", "proto", "cloud":
				return true
			}
		}
	}
	return false
}

// claudePID is a running Claude CLI from ~/.claude/sessions/<pid>.json.
type claudePID struct {
	PID      int
	Start    string
	Headless bool
}

// claudePIDs are the Claude CLIs that record uuid as their session.
func claudePIDs(uuid string) []claudePID {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "sessions", "*.json"))
	var out []claudePID
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var r struct {
			PID        int    `json:"pid"`
			SessionID  string `json:"sessionId"`
			ProcStart  string `json:"procStart"`
			Kind       string `json:"kind"`
			Entrypoint string `json:"entrypoint"`
		}
		if json.Unmarshal(b, &r) != nil || r.SessionID != uuid || r.PID <= 1 {
			continue
		}
		out = append(out, claudePID{PID: r.PID, Start: r.ProcStart,
			Headless: (r.Kind != "" && r.Kind != "interactive") || strings.HasPrefix(r.Entrypoint, "sdk")})
	}
	return out
}

// RealProcs is this Mac's process table, through ps and lsof.
type RealProcs struct{}

func (RealProcs) List(ctx context.Context) ([]Proc, error) {
	cmd := exec.CommandContext(ctx, "ps", "-axww", "-o", "pid=,ppid=,lstart=,args=")
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C") // lstart as Claude records procStart: "Thu Oct 15 17:52:32 2026", UTC
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var ps []Proc
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 8 { // pid ppid Thu Oct 15 17:52:32 2026 args…
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		ps = append(ps, Proc{PID: pid, PPID: ppid, Start: strings.Join(f[2:7], " "), Args: strings.Join(f[7:], " ")})
	}
	return ps, nil
}

func (RealProcs) OpenedBy(ctx context.Context, path string) []int {
	out, _ := exec.CommandContext(ctx, "lsof", "-t", "--", path).Output() // exit 1 when nobody has it open
	var pids []int
	for _, l := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(l); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

func (RealProcs) Cwd(ctx context.Context, pid int) string {
	out, _ := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "n") {
			return l[1:]
		}
	}
	return ""
}

func (RealProcs) Signal(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

func (RealProcs) Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
