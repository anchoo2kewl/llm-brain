package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Terminals: an interactive CLI (claude, codex, gemini, or a login shell) in a tmux session on this host,
// with no approvals and no sandbox. tmux keeps it running when the browser, the hub or this agent goes away;
// anyone on the Mac can attach with AttachCommand. The hub queues three commands:
//
//	term_open    make the tmux session in the folder and start the CLI in a login shell
//	term_attach  run `tmux attach` under a pty and dial the hub's websocket with the token; the hub pairs it
//	             with the browser. Binary frames are terminal bytes both ways; text frames are JSON control
//	             ({"t":"resize","cols":..,"rows":..}).
//	term_kill    end the tmux session
//
// The tmux server runs on its own socket (-L llm-brain) with its own config, so it never touches the
// person's own tmux.

const (
	KindTermOpen   = "term_open"
	KindTermAttach = "term_attach"
	KindTermKill   = "term_kill"
)

// TermProviders are what a terminal can run.
var TermProviders = []string{"claude", "codex", "gemini", "shell"}

func KnownTermProvider(p string) bool {
	for _, x := range TermProviders {
		if x == p {
			return true
		}
	}
	return false
}

func TermProviderName(p string) string {
	if p == "shell" {
		return "Shell"
	}
	return ProviderName(p)
}

// TermOpen is a term_open payload.
type TermOpen struct {
	Provider string    `json:"provider"`
	Cwd      string    `json:"cwd"` // absolute, or ~/...; '' = a scratch folder
	Model    string    `json:"model,omitempty"`
	Prompt   string    `json:"prompt,omitempty"` // the first message, typed into the CLI
	Worktree *Worktree `json:"worktree,omitempty"`
	Ask      bool      `json:"ask,omitempty"` // the CLI's normal approval prompts instead of every permission
}

// TermAttach is a term_attach payload.
type TermAttach struct {
	Token string `json:"token"`
	Cols  int    `json:"cols"`
	Rows  int    `json:"rows"`
}

const tmuxSocket = "llm-brain"

func TmuxName(id int64) string { return "llmb-" + strconv.FormatInt(id, 10) }

// AttachCommand is how to reach the terminal from a shell on that Mac.
func AttachCommand(id int64) string { return "tmux -L " + tmuxSocket + " attach -t " + TmuxName(id) }

const tmuxConf = `# written by the llm-brain hub agent; used only by its own tmux server (-L llm-brain)
set -g history-limit 100000
set -g mouse on
set -g status off
set -g escape-time 10
set -g default-terminal "xterm-256color"
set -g set-clipboard on
set -g focus-events on
set -g allow-passthrough on
setw -g aggressive-resize on
`

type tmux struct {
	bin, conf, sock string
}

// tmux finds tmux and writes its config under the state dir.
func (e *Executor) tmux() (tmux, error) {
	bin, err := lookPath("tmux", searchPath())
	if err != nil {
		return tmux{}, errors.New("tmux is not installed on this Mac (brew install tmux)")
	}
	conf := filepath.Join(e.StateDir, "tmux.conf")
	if b, err := os.ReadFile(conf); err != nil || string(b) != tmuxConf {
		if err := os.MkdirAll(e.StateDir, 0o755); err != nil {
			return tmux{}, err
		}
		if err := os.WriteFile(conf, []byte(tmuxConf), 0o644); err != nil {
			return tmux{}, err
		}
	}
	sock := e.TmuxSocket
	if sock == "" {
		sock = tmuxSocket
	}
	return tmux{bin: bin, conf: conf, sock: sock}, nil
}

// socketPath is where tmux -L puts the server's socket: $TMUX_TMPDIR (or /tmp)/tmux-<uid>/<name>.
func (t tmux) socketPath() string {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), t.sock)
}

func (t tmux) cmd(args ...string) *exec.Cmd {
	c := exec.Command(t.bin, append([]string{"-L", t.sock, "-f", t.conf}, args...)...)
	c.Env = termEnv(nil)
	return c
}

func (t tmux) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, t.bin, append([]string{"-L", t.sock, "-f", t.conf}, args...)...)
	c.Env = termEnv(nil)
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("tmux %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// termEnv is the environment for tmux and the shells it starts: ours, minus anything that would make a
// CLI think it runs inside another one, with a full PATH and a real TERM.
func termEnv(extra []string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH", "TERM", "TMUX", "TMUX_PANE", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "CPA_API_KEY":
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "PATH="+searchPath(), "TERM=xterm-256color", "COLORTERM=truecolor")
	// launchd gives no locale; without a UTF-8 one tmux turns every non-ASCII byte (and a tab in -F
	// output) into '_', so the CLIs' glyphs and our own listing come out mangled.
	if os.Getenv("LC_ALL") == "" && os.Getenv("LC_CTYPE") == "" && !strings.Contains(strings.ToUpper(os.Getenv("LANG")), "UTF-8") {
		env = append(env, "LANG=en_US.UTF-8")
	}
	return append(env, extra...)
}

// Terminals lists this host's live terminals and the folder each started in; ok is false when the list
// can't be trusted (no tmux, or tmux failed), so the hub ends nothing on it.
func (e *Executor) Terminals(ctx context.Context) ([]store.TermSeen, bool) {
	t, err := e.tmux()
	if err != nil {
		return nil, false
	}
	out, err := t.run(ctx, "list-sessions", "-F", "#{session_name} #{session_path}")
	if err != nil {
		// Only a missing socket means "no terminals"; any other failure (a signal at shutdown, say) is unknown.
		if _, serr := os.Stat(t.socketPath()); errors.Is(serr, os.ErrNotExist) {
			return nil, true
		}
		return nil, false
	}
	var seen []store.TermSeen
	for _, l := range strings.Split(out, "\n") {
		name, cwd, _ := strings.Cut(strings.TrimSpace(l), " ") // our session names have no spaces
		if s, ok := strings.CutPrefix(name, "llmb-"); ok {
			if id, err := strconv.ParseInt(s, 10, 64); err == nil {
				seen = append(seen, store.TermSeen{ID: id, Cwd: cwd})
			}
		}
	}
	sort.Slice(seen, func(i, j int) bool { return seen[i].ID < seen[j].ID })
	return seen, true
}

// ErrTermEnded is the attach error that tells the hub the tmux session is gone.
const ErrTermEnded = "the terminal has ended"

// termLock serializes one terminal's open, kill and attach, so an End can't slip in before the Open it
// races and leave a session nobody manages.
func (e *Executor) termLock(id int64) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.termMu == nil {
		e.termMu = map[int64]*sync.Mutex{}
	}
	m := e.termMu[id]
	if m == nil {
		m = &sync.Mutex{}
		e.termMu[id] = m
	}
	return m
}

// cancelOpen ends an open still in progress for the terminal (making its worktree, say), and marks the
// terminal killed so an open that hasn't started yet (same poll batch) never makes the session.
func (e *Executor) cancelOpen(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.killed == nil {
		e.killed = map[int64]bool{}
	}
	e.killed[id] = true
	if c := e.opening[id]; c != nil {
		c()
	}
}

func (e *Executor) wasKilled(id int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.killed[id]
}

// TermScript is the shell command the tmux session runs: the CLI with every permission, then a login
// shell in the same folder when it exits, so the session stays a command line.
func TermScript(o TermOpen, gw Gateway, shell string) (string, []string, error) {
	if o.Model != "" && !modelRe.MatchString(o.Model) {
		return "", nil, fmt.Errorf("bad model name %q", o.Model)
	}
	var argv, env []string
	switch o.Provider {
	case "claude":
		argv = []string{"claude", "--dangerously-skip-permissions"}
		if o.Ask { // explicit, so a bypass default in the CLI's own settings doesn't win
			argv = []string{"claude", "--permission-mode", "manual"}
		}
		if o.Model != "" {
			argv = append(argv, "--model", o.Model)
		}
		if gw.On() {
			env = []string{"ANTHROPIC_BASE_URL=" + gw.URL, "ANTHROPIC_AUTH_TOKEN=" + gw.Key}
		}
	case "codex":
		argv = []string{"codex", "--dangerously-bypass-approvals-and-sandbox"}
		if o.Ask {
			argv = []string{"codex", "--ask-for-approval", "on-request", "--sandbox", "workspace-write"}
		}
		if o.Model != "" {
			argv = append(argv, "-m", o.Model)
		}
		if gw.On() {
			argv = append(argv, "-c", "model_provider=cpa",
				"-c", fmt.Sprintf(`model_providers.cpa={name="CPA",base_url="%s/v1",env_key="CPA_API_KEY",wire_api="responses",requires_openai_auth=false}`,
					strings.TrimRight(gw.URL, "/")))
			env = []string{"CPA_API_KEY=" + gw.Key}
		}
	case "gemini":
		argv = []string{"gemini", "--approval-mode", "yolo"}
		if o.Ask {
			argv = []string{"gemini", "--approval-mode", "default"}
		}
		if o.Model != "" {
			argv = append(argv, "--model", o.Model)
		}
	case "shell":
		return "exec " + shellQuote(shell) + " -l", nil, nil
	default:
		return "", nil, fmt.Errorf("unknown CLI %q", o.Provider)
	}
	if p := strings.TrimSpace(o.Prompt); p != "" {
		if o.Provider == "gemini" { // the = form keeps a prompt starting with '-' a value
			argv = append(argv, "--prompt-interactive="+p)
		} else {
			argv = append(argv, "--", p)
		}
	}
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ") + "; exec " + shellQuote(shell) + " -l", env, nil
}

func userShell() string {
	if s := os.Getenv("SHELL"); s != "" && filepath.IsAbs(s) {
		return s
	}
	return "/bin/zsh"
}

// termCwd resolves the folder a terminal runs in. A terminal is a full shell, so any folder in the home
// directory will do; a worktree is still made the usual way, under the roots.
func (e *Executor) termCwd(ctx context.Context, id int64, o TermOpen) (string, error) {
	home, _ := os.UserHomeDir()
	if o.Cwd == "" {
		d := filepath.Join(e.StateDir, "terminals", strconv.FormatInt(id, 10))
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(d)
	}
	want := expandHome(o.Cwd)
	if filepath.Clean(want) == filepath.Clean(home) {
		return filepath.EvalSymlinks(home)
	}
	cwd, err := NewRoots([]string{home}).Validate(want)
	if err != nil {
		if errors.Is(err, ErrOutside) {
			return "", fmt.Errorf("%s is not in your home folder", o.Cwd)
		}
		return "", err
	}
	if o.Worktree != nil {
		return MakeWorktree(ctx, e.Roots, cwd, o.Worktree.Base, o.Worktree.Slug)
	}
	return cwd, nil
}

func (e *Executor) termOpen(ctx context.Context, c store.Command) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.mu.Lock()
	if e.opening == nil {
		e.opening = map[int64]context.CancelFunc{}
	}
	e.opening[c.AgentID] = cancel
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.opening, c.AgentID)
		e.mu.Unlock()
	}()
	l := e.termLock(c.AgentID)
	l.Lock()
	defer l.Unlock()
	var o TermOpen
	if err := json.Unmarshal(c.Payload, &o); err != nil {
		e.report(ctx, c.ID, Report{Status: "failed", Error: "bad payload: " + err.Error()})
		return
	}
	fail := func(err error) {
		e.logf("agent: terminal %d: %v", c.AgentID, err)
		e.report(ctx, c.ID, Report{Status: "failed", Error: err.Error()})
	}
	if !KnownTermProvider(o.Provider) {
		fail(fmt.Errorf("unknown CLI %q", o.Provider))
		return
	}
	t, err := e.tmux()
	if err != nil {
		fail(err)
		return
	}
	cwd, err := e.termCwd(ctx, c.AgentID, o)
	if err == nil && (ctx.Err() != nil || e.wasKilled(c.AgentID)) {
		err = errors.New("ended before it started")
	}
	if err != nil {
		fail(err)
		return
	}
	gw := Gateway{}
	if e.Config.TerminalGateway && (o.Provider == "claude" || o.Provider == "codex") {
		gw = e.Config.ResolveGateway(ctx)
	}
	shell := userShell()
	script, env, err := TermScript(o, gw, shell)
	if err != nil {
		fail(err)
		return
	}
	args := []string{"new-session", "-d", "-s", TmuxName(c.AgentID), "-x", "200", "-y", "50", "-c", cwd}
	// LLM_BRAIN_REMOTE: nobody is at this Mac's screen, so a browser login must be approved elsewhere
	// (skills read it, e.g. aws-sso-login uses --use-device-code).
	env = append(env, "LLM_BRAIN_REMOTE=1", "LLM_BRAIN_TERMINAL="+strconv.FormatInt(c.AgentID, 10))
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	if o.Provider == "shell" {
		args = append(args, shell, "-l") // straight in: -lic + exec would run the startup files twice
	} else {
		// -i: an interactive login shell reads .zshrc too, so the CLI is on PATH however it was installed.
		args = append(args, shell, "-lic", script)
	}
	if ctx.Err() != nil || e.wasKilled(c.AgentID) { // an End came while it was getting ready
		fail(errors.New("ended before it started"))
		return
	}
	if _, err := t.run(context.WithoutCancel(ctx), args...); err != nil {
		fail(err)
		return
	}
	e.logf("agent: terminal %d: %s in %s (%s)", c.AgentID, o.Provider, cwd, TmuxName(c.AgentID))
	e.report(ctx, c.ID, Report{Status: "done", Cwd: cwd})
}

func (e *Executor) termKill(ctx context.Context, c store.Command) {
	e.cancelOpen(c.AgentID)
	l := e.termLock(c.AgentID)
	l.Lock()
	defer l.Unlock()
	t, err := e.tmux()
	if err != nil {
		e.report(ctx, c.ID, Report{Status: "failed", Error: err.Error()})
		return
	}
	if out, err := t.run(ctx, "kill-session", "-t", "="+TmuxName(c.AgentID)); err != nil && !strings.Contains(out, "can't find") {
		e.report(ctx, c.ID, Report{Status: "failed", Error: err.Error()})
		return
	}
	e.report(ctx, c.ID, Report{Status: "done"})
}

// termAttach bridges `tmux attach` to the hub. The command is done once the bridge is up; the bridge lives
// until either side closes, or this agent stops.
func (e *Executor) termAttach(ctx context.Context, c store.Command) {
	var a TermAttach
	if err := json.Unmarshal(c.Payload, &a); err != nil || a.Token == "" {
		e.report(ctx, c.ID, Report{Status: "failed", Error: "bad payload"})
		return
	}
	fail := func(err error) {
		e.logf("agent: terminal %d attach: %v", c.AgentID, err)
		e.report(ctx, c.ID, Report{Status: "failed", Error: err.Error()})
	}
	if e.HubURL == "" {
		fail(errors.New("this agent doesn't know the hub's address"))
		return
	}
	t, err := e.tmux()
	if err != nil {
		fail(err)
		return
	}
	name := TmuxName(c.AgentID)
	if _, err := t.run(ctx, "has-session", "-t", "="+name); err != nil {
		fail(errors.New(ErrTermEnded))
		return
	}
	u, err := url.Parse(strings.TrimRight(e.HubURL, "/") + "/api/agent/term/" + url.PathEscape(a.Token))
	if err != nil {
		fail(err)
		return
	}
	u.Scheme = map[string]string{"https": "wss"}[u.Scheme]
	if u.Scheme == "" {
		u.Scheme = "ws"
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	ws, _, err := websocket.Dial(dctx, u.String(), nil)
	cancel()
	if err != nil {
		fail(fmt.Errorf("dial hub: %w", err))
		return
	}
	ws.SetReadLimit(4 << 20)
	cmd := t.cmd("attach-session", "-t", "="+name)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: clampSize(a.Cols, 80), Rows: clampSize(a.Rows, 24)})
	if err != nil {
		ws.Close(websocket.StatusInternalError, "attach failed")
		fail(err)
		return
	}
	e.report(ctx, c.ID, Report{Status: "done"})
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		// Ending the bridge kills the attach client (only the client: the tmux session keeps running) and
		// closes the pty, which also frees a write stuck on a full pty.
		bridge(ctx, ws, ptmx, func() {
			_ = cmd.Process.Kill()
			_ = ptmx.Close()
		})
		_ = cmd.Wait()
	}()
}

func clampSize(n, def int) uint16 {
	if n < 10 || n > 1000 {
		return uint16(def)
	}
	return uint16(n)
}

// bridge copies pty output to the websocket and websocket input to the pty until either side ends, then
// calls stop (once). Input goes through a queue to its own writer, so a pty that stops draining can't block
// the websocket reader; stop closing the pty unblocks that writer.
func bridge(ctx context.Context, ws *websocket.Conn, ptmx *os.File, stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var once sync.Once
	done := func(code websocket.StatusCode, why string) {
		once.Do(func() {
			cancel()
			stop()
			_ = ws.Close(code, why)
		})
	}
	defer done(websocket.StatusGoingAway, "")
	go func() { // the agent stopping ends the bridge too
		<-ctx.Done()
		done(websocket.StatusGoingAway, "the agent stopped")
	}()
	in := make(chan []byte, 256)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-in:
				if _, err := ptmx.Write(b); err != nil {
					done(websocket.StatusInternalError, "pty write failed")
					return
				}
			}
		}
	}()
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				wctx, wc := context.WithTimeout(ctx, 30*time.Second)
				werr := ws.Write(wctx, websocket.MessageBinary, buf[:n])
				wc()
				if werr != nil {
					done(websocket.StatusGoingAway, "")
					return
				}
			}
			if err != nil {
				reason := "detached"
				if !errors.Is(err, io.EOF) {
					reason = ErrTermEnded
				}
				done(websocket.StatusNormalClosure, reason)
				return
			}
		}
	}()
	for {
		typ, b, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			select {
			case in <- b:
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second): // the pty hasn't taken input for 10 s
				done(websocket.StatusInternalError, "the terminal stopped reading input")
				return
			}
			continue
		}
		var m struct {
			T          string
			Cols, Rows int
		}
		if json.Unmarshal(b, &m) == nil && m.T == "resize" {
			_ = pty.Setsize(ptmx, &pty.Winsize{Cols: clampSize(m.Cols, 80), Rows: clampSize(m.Rows, 24)})
		}
	}
}

// homePlaces: the home folder and the folders directly in it, minus hidden ones and macOS's media/system
// folders, for the terminal folder picker.
func homePlaces() (string, []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil
	}
	skip := map[string]bool{"Library": true, "Applications": true, "Movies": true, "Music": true, "Pictures": true, "Public": true}
	ents, _ := os.ReadDir(home)
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, ".") || skip[n] || !e.IsDir() {
			continue
		}
		out = append(out, filepath.Join(home, n))
	}
	return home, out
}
