package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Runner starts processes. ProcRunner is the real one; tests use a fake that replays fixtures.
type Runner interface {
	Start(ctx context.Context, s Spec) (Process, error)
}

type Process interface {
	PID() int
	Output() io.Reader // stdout and stderr, merged
	Wait() (int, error)
	Stop()  // end the whole process group
	Close() // stop reading output
}

// Executor runs this host's commands: one goroutine per turn, at most MaxTurns at once.
type Executor struct {
	Host, Version string
	Hub           Hub
	Roots         Roots
	Brain         string // llm-brain checkout: config/fleet.yaml, bin/review-rules
	StateDir      string // turn logs go to <state>/logs (shipped like worker logs)
	Config        Config
	Runner        Runner
	Log           *log.Logger
	Poll          time.Duration
	MaxTurns      int
	Providers     []string // the CLIs to offer; nil = whichever are on PATH
	HubURL        string   // where terminal bridges dial back (term.go); '' = no terminals
	TmuxSocket    string   // tmux -L name for terminals; '' = llm-brain

	mu      sync.Mutex
	active  map[int64]*turn              // by turn command id
	busy    map[int64]bool               // terminal commands in hand, so the hub doesn't fail a slow one as lost
	termMu  map[int64]*sync.Mutex        // per terminal: open, kill and attach one at a time (term.go)
	opening map[int64]context.CancelFunc // per terminal: the open in progress, for a kill to cancel
	killed  map[int64]bool               // terminals this agent was told to end
	wg      sync.WaitGroup
}

// turn is a turn this executor holds, from the moment it is claimed (so it counts against MaxTurns and a
// Stop finds it) until its final report.
type turn struct {
	agentID int64
	cancel  context.CancelFunc // ends worktree creation and anything else before the process starts
	proc    Process            // nil until the process has started
	stopped bool
}

// halt marks t stopped and ends whatever stage it is in. Call with e.mu held.
func (t *turn) halt() {
	t.stopped = true
	t.cancel()
	if t.proc != nil {
		t.proc.Stop()
	}
}

func (e *Executor) logf(f string, a ...any) {
	if e.Log != nil {
		e.Log.Printf(f, a...)
	}
}

// RunningKeys are the run keys of live turns ("agent-<session>"), for the shipper's heartbeat, so a turn's
// log shows as a running run.
func (e *Executor) RunningKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, t := range e.active {
		out = append(out, "agent-"+strconv.FormatInt(t.agentID, 10))
	}
	sort.Strings(out)
	return out
}

func (e *Executor) runningIDs() []int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []int64
	for id := range e.active {
		out = append(out, id)
	}
	for id := range e.busy {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (e *Executor) init() {
	e.mu.Lock()
	if e.active == nil {
		e.active = map[int64]*turn{}
		e.busy = map[int64]bool{}
	}
	e.mu.Unlock()
	if e.Poll <= 0 {
		e.Poll = 3 * time.Second
	}
	if e.MaxTurns <= 0 {
		e.MaxTurns = 6
	}
	if e.Runner == nil {
		e.Runner = ProcRunner{}
	}
}

// Run polls until ctx ends, then stops the turns still running.
func (e *Executor) Run(ctx context.Context) {
	e.init()
	e.logf("agent: running sessions on %s under %s", e.Host, strings.Join(e.Roots.Paths(), ", "))
	var lastInv time.Time
	for ctx.Err() == nil {
		if time.Since(lastInv) > time.Minute {
			if err := e.Hub.Inventory(ctx, e.inventory(ctx)); err != nil && ctx.Err() == nil {
				e.logf("agent: inventory: %v", err)
			} else {
				lastInv = time.Now()
			}
		}
		if err := e.PollOnce(ctx); err != nil && ctx.Err() == nil {
			e.logf("agent: poll: %v", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(e.Poll):
		}
	}
	e.mu.Lock()
	for _, t := range e.active {
		t.halt()
	}
	e.mu.Unlock()
	e.wg.Wait() // every turn has stopped its process group and sent its final report
}

func (e *Executor) inventory(ctx context.Context) Inventory {
	provs := e.Providers
	if provs == nil {
		provs = AvailableProviders()
	}
	inv := Inventory{Host: e.Host, Version: e.Version, Roots: e.Roots.Paths(), Folders: e.Roots.Scan(1000), Providers: provs}
	if e.HubURL != "" {
		inv.Home, inv.Places = homePlaces()
		inv.TermsAt = time.Now()
		inv.Terminals, inv.Tmux = e.Terminals(ctx)
		if ctx.Err() != nil { // stopping: a listing cut short proves nothing
			inv.Terminals, inv.Tmux = nil, false
		}
	}
	return inv
}

// PollOnce fetches and starts this host's new commands.
func (e *Executor) PollOnce(ctx context.Context) error {
	e.init()
	cmds, err := e.Hub.Poll(ctx, e.Host, e.Version, e.runningIDs())
	if err != nil {
		return err
	}
	for _, c := range cmds {
		e.logf("agent: command %d: %s for session %d", c.ID, c.Kind, c.AgentID)
		switch c.Kind {
		case KindTurn:
			// Reserve the slot before the goroutine starts, so a batch can't overrun MaxTurns and a Stop
			// that follows in the same batch finds the turn.
			tctx, cancel := context.WithCancel(ctx)
			e.mu.Lock()
			n := len(e.active)
			if n >= e.MaxTurns {
				e.mu.Unlock()
				cancel()
				e.report(ctx, c.ID, Report{Status: "failed", Error: fmt.Sprintf("%s already runs %d turns", e.Host, n)})
				continue
			}
			st := &turn{agentID: c.AgentID, cancel: cancel}
			e.active[c.ID] = st
			e.mu.Unlock()
			e.wg.Add(1)
			go func(c store.Command) {
				defer e.wg.Done()
				defer func() {
					e.mu.Lock()
					delete(e.active, c.ID)
					e.mu.Unlock()
					cancel()
				}()
				e.runTurn(tctx, c, st)
			}(c)
		case KindStop:
			e.stop(ctx, c)
		case KindTermOpen, KindTermAttach, KindTermKill:
			e.mu.Lock()
			e.busy[c.ID] = true
			e.mu.Unlock()
			e.wg.Add(1)
			go func(c store.Command) {
				defer func() {
					e.mu.Lock()
					delete(e.busy, c.ID)
					e.mu.Unlock()
					e.wg.Done()
				}()
				map[string]func(context.Context, store.Command){
					KindTermOpen: e.termOpen, KindTermAttach: e.termAttach, KindTermKill: e.termKill,
				}[c.Kind](ctx, c)
			}(c)
		default:
			e.report(ctx, c.ID, Report{Status: "failed", Error: "unknown command " + c.Kind})
		}
	}
	return nil
}

// Wait waits for every started turn to finish (tests).
func (e *Executor) Wait() { e.wg.Wait() }

// report delivers a report, retrying a few times; a final report is still tried after ctx ends.
func (e *Executor) report(ctx context.Context, id int64, r Report) {
	rctx := ctx
	if r.Final() {
		var cancel context.CancelFunc
		rctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
	}
	for i := 0; ; i++ {
		err := e.Hub.Report(rctx, id, r)
		if err == nil {
			return
		}
		if i >= 4 || rctx.Err() != nil {
			e.logf("agent: report %d (%s): %v", id, r.Status, err)
			return
		}
		select {
		case <-rctx.Done():
		case <-time.After(time.Duration(i+1) * 2 * time.Second):
		}
	}
}

func (e *Executor) stop(ctx context.Context, c store.Command) {
	var p Stop
	_ = json.Unmarshal(c.Payload, &p)
	e.mu.Lock()
	hit := false
	for id, t := range e.active {
		if (p.TurnCommand > 0 && id == p.TurnCommand) || (p.TurnCommand == 0 && t.agentID == c.AgentID) {
			hit = true
			t.halt() // also before the process exists: the turn checks before it starts one
		}
	}
	e.mu.Unlock()
	if !hit {
		e.report(ctx, c.ID, Report{Status: "failed", Error: "no running turn for this session on " + e.Host})
		return
	}
	e.report(ctx, c.ID, Report{Status: "done"})
}

func (e *Executor) runTurn(ctx context.Context, c store.Command, st *turn) {
	stopped := func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return st.stopped
	}
	fail := func(err error) {
		if stopped() {
			e.report(ctx, c.ID, Report{Status: "stopped"})
			return
		}
		e.logf("agent: command %d failed: %v", c.ID, err)
		e.report(ctx, c.ID, Report{Status: "failed", Error: err.Error()})
	}
	errStopped := errors.New("stopped")
	var t Turn
	if err := json.Unmarshal(c.Payload, &t); err != nil {
		fail(fmt.Errorf("bad payload: %w", err))
		return
	}
	if !KnownProvider(t.Provider) {
		fail(fmt.Errorf("unknown provider %q", t.Provider))
		return
	}
	var cwd string
	var err error
	switch {
	case t.Scratch && t.Resume == "" && t.Cwd == "":
		cwd, err = e.scratchDir(c.AgentID)
	case t.Scratch:
		cwd, err = NewRoots([]string{e.chatsDir()}).Validate(t.Cwd)
	case t.InHome && (t.Review != nil || normPerm(t.Perm) == PermPlan):
		// The roots as usual, else anywhere in home, home itself included.
		if cwd, err = e.Roots.Validate(t.Cwd); err != nil {
			home, _ := os.UserHomeDir()
			realHome, _ := filepath.EvalSymlinks(home)
			if c := filepath.Clean(t.Cwd); c == filepath.Clean(home) || c == realHome {
				cwd, err = realHome, nil
			} else {
				cwd, err = NewRoots([]string{home}).Validate(t.Cwd)
			}
		}
	default:
		cwd, err = e.Roots.Validate(t.Cwd)
	}
	if err != nil {
		fail(err)
		return
	}
	if stopped() {
		fail(errStopped)
		return
	}
	if t.Worktree != nil && t.Resume == "" {
		if cwd, err = MakeWorktree(ctx, e.Roots, cwd, t.Worktree.Base, t.Worktree.Slug); err != nil {
			fail(err)
			return
		}
	}
	prompt := t.Prompt
	if t.Review != nil {
		base := t.Review.Base
		if base == "" {
			base = DefaultBase(ctx, cwd)
		}
		if !ValidRef(base) {
			fail(fmt.Errorf("bad base branch %q", base))
			return
		}
		t.Perm, t.Bypass = PermPlan, false
		if t.Resume == "" || prompt == "" {
			prompt = BuildReviewPrompt(ctx, e.Brain, cwd, base)
		}
	}
	if strings.TrimSpace(prompt) == "" {
		fail(errors.New("empty prompt"))
		return
	}
	stamp := time.Now().Format("20060102-150405")
	logDir := filepath.Join(e.StateDir, "logs")
	tmpDir := filepath.Join(e.StateDir, "agent")
	_ = os.MkdirAll(logDir, 0o755)
	_ = os.MkdirAll(tmpDir, 0o700)
	logPath := filepath.Join(logDir, fmt.Sprintf("agent-%d.%s.log", c.AgentID, stamp))
	gw := Gateway{}
	if t.Provider != "gemini" {
		gw = e.Config.ResolveGateway(ctx)
	}
	route := "the CLI's own login"
	if gw.On() {
		route = "gateway " + gw.URL
	}
	var spec Spec
	switch t.Provider {
	case "claude":
		spec, err = BuildClaude(t, cwd, prompt, gw)
	case "codex":
		spec, err = BuildCodex(t, cwd, prompt, filepath.Join(tmpDir, fmt.Sprintf("%d.last", c.ID)), gw)
	default:
		spec, err = BuildGemini(t, cwd, prompt)
		route = "the CLI's own login"
	}
	if err != nil {
		fail(err)
		return
	}
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fail(err)
		return
	}
	defer lf.Close()
	fmt.Fprintf(lf, "%s hub session %d, command %d, %s in %s via %s\n$ %s %s\n\n", time.Now().Format("2006-01-02 15:04:05"),
		c.AgentID, c.ID, t.Provider, cwd, route, spec.Bin, strings.Join(shownArgs(spec.Args), " "))
	if stopped() || ctx.Err() != nil {
		fmt.Fprintln(lf, "● stopped before it started")
		fail(errStopped)
		return
	}
	proc, err := e.Runner.Start(ctx, spec)
	if err != nil {
		fmt.Fprintf(lf, "✗ start: %v\n", err)
		fail(err)
		return
	}
	e.mu.Lock()
	st.proc = proc
	if st.stopped { // a Stop came while it was starting
		proc.Stop()
	}
	e.mu.Unlock()
	e.report(ctx, c.ID, Report{Status: "running", PID: proc.PID(), Cwd: cwd, Log: logPath, SessionID: t.Resume})

	capt := &Capture{Provider: t.Provider, SessionID: t.Resume}
	var tail []string // the last lines that weren't events (stderr), for the error message
	done := make(chan struct{})
	go func() {
		defer close(done)
		br := bufio.NewReaderSize(proc.Output(), 1<<20)
		known := capt.SessionID
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				out := capt.Line(line)
				if out != "" {
					fmt.Fprintln(lf, out)
					if out == strings.TrimRight(line, "\r\n") && !strings.HasPrefix(strings.TrimSpace(line), "{") {
						tail = append(tail, out)
						if len(tail) > 15 {
							tail = tail[1:]
						}
					}
				}
				if capt.SessionID != known {
					known = capt.SessionID
					e.report(ctx, c.ID, Report{Status: "running", PID: proc.PID(), Cwd: cwd, Log: logPath, SessionID: known})
				}
			}
			if err != nil {
				return
			}
		}
	}()
	code, werr := proc.Wait()
	select {
	case <-done:
	case <-time.After(5 * time.Second): // a grandchild still holds the pipe
	}
	proc.Close()
	<-done
	if esc, ok := proc.(interface{ Escalated() <-chan struct{} }); ok && stopped() {
		<-esc.Escalated() // the whole group is gone, descendants included
	}

	final := capt.Final
	if spec.LastFile != "" {
		if b, err := os.ReadFile(spec.LastFile); err == nil && strings.TrimSpace(string(b)) != "" {
			final = strings.TrimSpace(string(b))
		}
		_ = os.Remove(spec.LastFile)
	}
	r := Report{Status: "done", SessionID: capt.SessionID, Cwd: cwd, ExitCode: code, Result: final, Log: logPath}
	switch {
	case stopped():
		r.Status = "stopped"
	case werr != nil || code != 0:
		r.Status = "failed"
		msg := capt.Err
		if msg == "" {
			msg = strings.Join(tail, "\n")
		}
		if werr != nil {
			msg = strings.TrimSpace(werr.Error() + "\n" + msg)
		}
		r.Error = strings.TrimSpace(fmt.Sprintf("exit %d\n%s", code, msg))
	}
	fmt.Fprintf(lf, "\n%s turn %s (exit %d)\n", time.Now().Format("2006-01-02 15:04:05"), r.Status, code)
	e.report(ctx, c.ID, r)
}

// shownArgs is the command line for the log, with an inline prompt shortened.
func shownArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if p, ok := strings.CutPrefix(a, "--prompt="); ok {
			a = "--prompt=" + strconv.Quote(oneLine(p, 60))
		}
		out[i] = a
	}
	return out
}

// AvailableProviders are the agent CLIs found on this machine.
func AvailableProviders() []string {
	var out []string
	for _, p := range Providers {
		if _, err := lookPath(p, searchPath()); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func (e *Executor) chatsDir() string { return filepath.Join(e.StateDir, "chats") }

// scratchDir makes <state>/chats/<session> for a chat with no project folder.
func (e *Executor) scratchDir(agentID int64) (string, error) {
	d := filepath.Join(e.chatsDir(), strconv.FormatInt(agentID, 10))
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(d)
}

// ---------------------------------------------------------------- the real runner

// ProcRunner starts the CLI in its own process group, so Stop ends it and everything it spawned: SIGTERM to
// the group, then SIGKILL to the group after Grace (default 5 s), whether or not the leader has exited.
type ProcRunner struct {
	Grace time.Duration
}

// searchPath is PATH plus where Homebrew and the claude installer put things (launchd gives a bare PATH).
func searchPath() string {
	home, _ := os.UserHomeDir()
	return strings.Join([]string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".local/bin"), os.Getenv("PATH")}, ":")
}

func lookPath(bin, path string) (string, error) {
	for _, d := range filepath.SplitList(path) {
		if d == "" {
			continue
		}
		p := filepath.Join(d, bin)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found on PATH", bin)
}

func childEnv(extra []string) []string {
	path := searchPath()
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "CPA_API_KEY":
			continue
		}
		env = append(env, kv)
	}
	return append(append(env, "PATH="+path), extra...)
}

func (r ProcRunner) Start(ctx context.Context, s Spec) (Process, error) {
	exe, err := lookPath(s.Bin, searchPath())
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = childEnv(s.Env)
	cmd.Stdin = strings.NewReader(s.Stdin)
	cmd.Stdout, cmd.Stderr = pw, pw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	pw.Close()
	grace := r.Grace
	if grace <= 0 {
		grace = 5 * time.Second
	}
	p := &proc{cmd: cmd, out: pr, exited: make(chan struct{}), escalated: make(chan struct{}), grace: grace}
	return p, nil
}

type proc struct {
	cmd       *exec.Cmd
	out       *os.File
	exited    chan struct{}
	escalated chan struct{} // closed once Stop's SIGKILL went out
	grace     time.Duration
	once      sync.Once
}

func (p *proc) Escalated() <-chan struct{} { return p.escalated }

func (p *proc) PID() int          { return p.cmd.Process.Pid }
func (p *proc) Output() io.Reader { return p.out }
func (p *proc) Close()            { _ = p.out.Close() }

func (p *proc) Wait() (int, error) {
	err := p.cmd.Wait()
	close(p.exited)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// Stop sends SIGTERM to the process group, then SIGKILL to the group after the grace period. The leader
// exiting early doesn't end it: a descendant that ignored SIGTERM is still in the group.
func (p *proc) Stop() {
	p.once.Do(func() {
		pgid := p.cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		go func() {
			defer close(p.escalated)
			time.Sleep(p.grace)
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}()
	})
}

func normPerm(p string) string {
	n, _ := NormPerm(p)
	return n
}
