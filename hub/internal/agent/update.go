package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/parse"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Tool updates. Every host agent reports the CLIs it can update (Homebrew, Claude Code, Codex, Gemini CLI, …)
// and how each was installed; the hub queues a tool_update command once a day (Updater) or when someone
// presses "Update now". The agent runs the updates one after another and reports each tool's version before
// and after, with the tail of its output.

const KindToolUpdate = store.KindToolUpdate

// Tool is a CLI the agent knows how to find and update.
type Tool struct {
	Name  string // as shown and stored: brew, claude, codex, gemini, gh, ocr
	Bin   string // the executable
	Label string
}

// Tools are the CLIs the hub offers to keep up to date, in update order: Homebrew itself first, since the
// casks and formulae below come from it.
var Tools = []Tool{
	{"brew", "brew", "Homebrew (update, then upgrade everything outdated)"},
	{"claude", "claude", "Claude Code"},
	{"codex", "codex", "Codex CLI"},
	{"gemini", "gemini", "Gemini CLI"},
	{"gh", "gh", "GitHub CLI"},
	{"ocr", "ocr", "open-code-review"},
}

// ToolNames are the names of Tools.
func ToolNames() []string {
	out := make([]string, len(Tools))
	for i, t := range Tools {
		out[i] = t.Name
	}
	return out
}

func toolByName(n string) (Tool, bool) {
	for _, t := range Tools {
		if t.Name == n {
			return t, true
		}
	}
	return Tool{}, false
}

// ToolUpdate is a tool_update command's payload.
type ToolUpdate struct {
	Tools []string `json:"tools"`
	Auto  bool     `json:"auto,omitempty"` // queued by the daily schedule, not by hand
}

// ToolResult is one tool's outcome.
type ToolResult struct {
	Name   string `json:"name"`
	Method string `json:"method,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	OK     bool   `json:"ok"`
	Skip   string `json:"skip,omitempty"` // why it wasn't updated (not installed, unknown install)
	Output string `json:"output,omitempty"`
	Secs   int    `json:"secs,omitempty"`
}

// Changed: the version moved.
func (r ToolResult) Changed() bool { return r.OK && r.After != "" && r.Before != r.After }

// UpdateResult is a tool_update command's result (Report.Result, as JSON).
type UpdateResult struct {
	Tools []ToolResult `json:"tools"`
}

// ParseUpdateResult reads a finished tool_update command's stored report.
func ParseUpdateResult(body string) (UpdateResult, string) {
	var rep Report
	if json.Unmarshal([]byte(body), &rep) != nil {
		return UpdateResult{}, ""
	}
	var out UpdateResult
	_ = json.Unmarshal([]byte(rep.Result), &out)
	return out, rep.Error
}

// ---------------------------------------------------------------- detection

var (
	caskRe    = regexp.MustCompile(`/Caskroom/([^/]+)/`)
	cellarRe  = regexp.MustCompile(`/Cellar/([^/]+)/`)
	npmRe     = regexp.MustCompile(`/lib/node_modules/((?:@[^/]+/)?[^/]+)/`)
	versionRe = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?(?:[-.][0-9A-Za-z]+)*`)
)

// InstallMethod says how the executable at real (symlinks resolved) was installed, so it can be updated the
// same way. ” means unknown: the hub shows it but won't touch it.
func InstallMethod(name, real, home string) string {
	if name == "brew" {
		return "brew"
	}
	if m := caskRe.FindStringSubmatch(real); m != nil {
		return "cask:" + m[1]
	}
	if m := cellarRe.FindStringSubmatch(real); m != nil {
		return "formula:" + m[1]
	}
	if m := npmRe.FindStringSubmatch(real); m != nil {
		return "npm:" + m[1]
	}
	// Claude's own installer: ~/.local/share/claude/versions/<v>, linked from ~/.local/bin/claude.
	if name == "claude" && home != "" && strings.HasPrefix(real, filepath.Join(home, ".local", "share", "claude")+string(filepath.Separator)) {
		return "self"
	}
	return ""
}

// Version picks the version out of `<tool> --version`.
func Version(out string) string {
	return versionRe.FindString(out)
}

// UpdateRunner runs one command for the updater, with output merged; tests replace it.
type UpdateRunner func(ctx context.Context, env []string, name string, args ...string) (string, error)

func realUpdateRun(ctx context.Context, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdin = nil // never wait on a prompt (sudo, a cask's installer): fail instead
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// toolState caches tool detection, which runs a few `--version`s; it's refreshed every 10 minutes and right
// after an update.
type toolState struct {
	mu       sync.Mutex
	at       time.Time
	tools    []store.ToolInfo
	updating sync.Mutex // one update at a time on this host (brew holds a lock anyway)
}

func (e *Executor) runner() UpdateRunner {
	if e.UpdateRun != nil {
		return e.UpdateRun
	}
	return realUpdateRun
}

// DetectTools finds every known tool on this host.
func (e *Executor) DetectTools(ctx context.Context) []store.ToolInfo {
	home, _ := os.UserHomeDir()
	path := searchPath()
	env := childEnv(nil)
	var out []store.ToolInfo
	for _, t := range Tools {
		p, err := lookPath(t.Bin, path)
		if err != nil {
			continue
		}
		info := store.ToolInfo{Name: t.Name, Path: p}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			real = p
		}
		info.Method = InstallMethod(t.Name, real, home)
		vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		v, err := e.runner()(vctx, env, p, "--version")
		cancel()
		if info.Version = Version(v); info.Version == "" && err != nil {
			info.Error = parse.Truncate(strings.TrimSpace(err.Error()), 200)
		}
		out = append(out, info)
	}
	return out
}

// tools is the cached detection, refreshed when older than 10 minutes.
func (e *Executor) tools(ctx context.Context, fresh bool) []store.ToolInfo {
	e.ts.mu.Lock()
	defer e.ts.mu.Unlock()
	if fresh || e.ts.tools == nil || time.Since(e.ts.at) > 10*time.Minute {
		find := e.DetectTools
		if e.FindTools != nil {
			find = e.FindTools
		}
		if ts := find(ctx); ctx.Err() == nil {
			e.ts.tools, e.ts.at = ts, time.Now()
		}
	}
	if e.ts.tools == nil {
		return []store.ToolInfo{}
	}
	return e.ts.tools
}

// ---------------------------------------------------------------- running an update

// toolUpdate runs a tool_update command: `brew update` once if anything here comes from Homebrew, then each
// tool's own update, in Tools order.
func (e *Executor) toolUpdate(ctx context.Context, c store.Command) {
	var p ToolUpdate
	if err := json.Unmarshal(c.Payload, &p); err != nil || len(p.Tools) == 0 {
		e.report(ctx, c.ID, Report{Status: "failed", Error: "bad payload"})
		return
	}
	if !e.ts.updating.TryLock() {
		e.report(ctx, c.ID, Report{Status: "failed", Error: "an update is already running on " + e.Host})
		return
	}
	defer e.ts.updating.Unlock()
	e.report(ctx, c.ID, Report{Status: "running"})
	want := map[string]bool{}
	for _, n := range p.Tools {
		want[n] = true
	}
	before := map[string]store.ToolInfo{}
	for _, t := range e.tools(ctx, true) {
		before[t.Name] = t
	}
	run := e.runner()
	env := childEnv([]string{"HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1", "NONINTERACTIVE=1", "CI=1"})
	brew := before["brew"].Path
	if brew == "" {
		brew, _ = lookPath("brew", searchPath())
	}
	step := func(timeout time.Duration, name string, args ...string) (string, error) {
		sctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		out, err := run(sctx, env, name, args...)
		if sctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("timed out after %s", timeout)
		}
		return out, err
	}
	var res UpdateResult
	var failed []string
	brewUpdated := false
	for _, t := range Tools {
		if !want[t.Name] || ctx.Err() != nil {
			continue
		}
		info, ok := before[t.Name]
		r := ToolResult{Name: t.Name, Method: info.Method, Before: info.Version}
		switch {
		case !ok:
			r.Skip = "not installed"
		case info.Method == "":
			r.Skip = "installed some other way; update it by hand"
		}
		if r.Skip != "" {
			res.Tools = append(res.Tools, r)
			continue
		}
		var cmds [][]string
		kind, arg, _ := strings.Cut(info.Method, ":")
		if (kind == "brew" || kind == "cask" || kind == "formula") && !brewUpdated {
			if brew == "" {
				r.Skip = "Homebrew isn't installed"
				res.Tools = append(res.Tools, r)
				continue
			}
			cmds = append(cmds, []string{brew, "update", "--quiet"})
			brewUpdated = true
		}
		switch kind {
		case "brew":
			cmds = append(cmds, []string{brew, "upgrade"})
		case "cask":
			cmds = append(cmds, []string{brew, "upgrade", "--cask", arg})
		case "formula":
			cmds = append(cmds, []string{brew, "upgrade", "--formula", arg})
		case "npm":
			npm, err := lookPath("npm", searchPath())
			if err != nil {
				r.Skip = "npm isn't installed"
				res.Tools = append(res.Tools, r)
				continue
			}
			cmds = append(cmds, []string{npm, "install", "-g", arg + "@latest"})
		case "self":
			cmds = append(cmds, []string{info.Path, "update"})
		}
		start := time.Now()
		var out strings.Builder
		var err error
		for _, cmd := range cmds {
			fmt.Fprintf(&out, "$ %s\n", strings.Join(append([]string{filepath.Base(cmd[0])}, cmd[1:]...), " "))
			var o string
			o, err = step(20*time.Minute, cmd[0], cmd[1:]...)
			out.WriteString(o)
			if err != nil {
				fmt.Fprintf(&out, "\n%v\n", err)
				break
			}
		}
		r.Secs = int(time.Since(start).Seconds())
		r.OK = err == nil
		r.Output = tail(out.String(), 6000)
		if !r.OK {
			failed = append(failed, t.Name)
		}
		res.Tools = append(res.Tools, r)
	}
	after := map[string]string{}
	for _, t := range e.tools(context.WithoutCancel(ctx), true) {
		after[t.Name] = t.Version
	}
	for i := range res.Tools {
		res.Tools[i].After = after[res.Tools[i].Name]
	}
	// Tell the hub the new versions now, not at the next inventory.
	if err := e.Hub.Inventory(context.WithoutCancel(ctx), e.inventory(context.WithoutCancel(ctx))); err != nil {
		e.logf("agent: inventory after update: %v", err)
	}
	body, _ := json.Marshal(res)
	rep := Report{Status: "done", Result: string(body)}
	switch {
	case ctx.Err() != nil:
		rep.Status, rep.Error = "stopped", "the agent stopped"
	case len(failed) > 0:
		rep.Status, rep.Error = "failed", "failed: "+strings.Join(failed, ", ")
	}
	e.report(ctx, c.ID, rep)
}

// tail keeps the last n bytes of s, on a line boundary when it can.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < 200 {
		s = s[i+1:]
	}
	return "…\n" + s
}

// ---------------------------------------------------------------- the hub's side

// UpdateSettings is how tool updates are configured (the settings table).
type UpdateSettings struct {
	Auto  bool
	Hour  int
	Tools []string
	Skip  []string // hosts the daily update leaves alone
}

func (u UpdateSettings) Skips(host string) bool {
	for _, h := range u.Skip {
		if h == host {
			return true
		}
	}
	return false
}

func (u UpdateSettings) Wants(tool string) bool {
	for _, t := range u.Tools {
		if t == tool {
			return true
		}
	}
	return false
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// LoadUpdateSettings reads the settings, with defaults: off, 4 am, every tool.
func LoadUpdateSettings(ctx context.Context, s *store.Store) (UpdateSettings, error) {
	var u UpdateSettings
	var errs []error
	get := func(k string) string {
		v, err := s.Setting(ctx, k)
		errs = append(errs, err)
		return v
	}
	u.Auto = get(store.KeyUpdateAuto) == "1"
	u.Hour = 4
	if h := get(store.KeyUpdateHour); h != "" {
		var n int
		if _, err := fmt.Sscan(h, &n); err == nil && n >= 0 && n <= 23 {
			u.Hour = n
		}
	}
	if t := get(store.KeyUpdateTools); t != "" {
		for _, n := range splitList(t) {
			if _, ok := toolByName(n); ok {
				u.Tools = append(u.Tools, n)
			}
		}
		if t == "-" { // none ticked
			u.Tools = []string{}
		}
	} else {
		u.Tools = ToolNames()
	}
	u.Skip = splitList(get(store.KeyUpdateSkip))
	return u, errors.Join(errs...)
}

// SaveUpdateSettings writes them.
func SaveUpdateSettings(ctx context.Context, s *store.Store, u UpdateSettings) error {
	auto := ""
	if u.Auto {
		auto = "1"
	}
	tools := strings.Join(u.Tools, ",")
	if tools == "" {
		tools = "-"
	}
	if u.Hour < 0 || u.Hour > 23 {
		return bad("the hour must be 0–23")
	}
	return s.SetSettings(ctx, map[string]string{store.KeyUpdateAuto: auto, store.KeyUpdateHour: fmt.Sprint(u.Hour),
		store.KeyUpdateTools: tools, store.KeyUpdateSkip: strings.Join(u.Skip, ",")})
}

// QueueToolUpdate queues an update of tools on host, unless one is already queued or running there.
func QueueToolUpdate(ctx context.Context, s *store.Store, host string, tools []string, auto bool) (int64, error) {
	var keep []string
	for _, n := range tools {
		if _, ok := toolByName(n); ok {
			keep = append(keep, n)
		}
	}
	if len(keep) == 0 {
		return 0, bad("pick at least one tool to update")
	}
	h, err := s.AgentHost(ctx, host)
	if errors.Is(err, store.ErrNotFound) {
		return 0, bad("no agent has run on %s", host)
	} else if err != nil {
		return 0, err
	}
	if !h.Live() {
		return 0, bad("the agent on %s isn't running", host)
	}
	if busy, err := s.ToolUpdateActive(ctx, host); err != nil {
		return 0, err
	} else if busy {
		return 0, bad("%s is already updating", host)
	}
	payload, _ := json.Marshal(ToolUpdate{Tools: keep, Auto: auto})
	return s.QueueCommand(ctx, store.Command{Host: host, Kind: KindToolUpdate, Payload: payload})
}

// Updater queues the daily update: once per local day, after the configured hour, on every live host that
// isn't skipped. A host that's offline then is updated when its agent comes back that day.
type Updater struct {
	S   *store.Store
	Log *log.Logger
	Now func() time.Time
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Updater) Run(ctx context.Context, every time.Duration) {
	for {
		if err := u.Tick(ctx); err != nil && ctx.Err() == nil && u.Log != nil {
			u.Log.Printf("updates: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Tick queues whatever is due now; it returns the hosts it queued.
func (u *Updater) Tick(ctx context.Context) error {
	set, err := LoadUpdateSettings(ctx, u.S)
	if err != nil || !set.Auto || len(set.Tools) == 0 {
		return err
	}
	now := u.now()
	if now.Hour() < set.Hour {
		return nil
	}
	today := now.Format("2006-01-02")
	hosts, err := u.S.AgentHosts(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, h := range hosts {
		if !h.Live() || set.Skips(h.Host) {
			continue
		}
		if last, err := u.S.UpdateLast(ctx, h.Host); err != nil || last == today {
			errs = append(errs, err)
			continue
		}
		id, err := QueueToolUpdate(ctx, u.S, h.Host, set.Tools, true)
		var ie InputError
		if errors.As(err, &ie) { // already updating (by hand): count it as today's
			err = nil
		} else if err != nil {
			errs = append(errs, err)
			continue
		} else if u.Log != nil {
			u.Log.Printf("updates: queued daily update %d on %s", id, h.Host)
		}
		errs = append(errs, u.S.SetUpdateLast(ctx, h.Host, now))
	}
	return errors.Join(errs...)
}
