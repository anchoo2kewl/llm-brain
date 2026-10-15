package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func has(args []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		if slices.Equal(args[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- argument builders

func TestBuildClaude(t *testing.T) {
	gw := Gateway{URL: "http://127.0.0.1:8317", Key: "test-key"}
	s, err := BuildClaude(Turn{Provider: "claude", Perm: PermEdit, Model: "sonnet", Name: "hub-7"}, "/w/app", "do it", gw)
	if err != nil {
		t.Fatal(err)
	}
	if s.Bin != "claude" || s.Dir != "/w/app" || s.Stdin != "do it" {
		t.Fatalf("%+v", s)
	}
	for _, want := range [][]string{{"-p"}, {"--output-format", "stream-json"}, {"--verbose"}, {"--name", "hub-7"},
		{"--model", "sonnet"}, {"--permission-mode", "acceptEdits"}} {
		if !has(s.Args, want...) {
			t.Errorf("args %v missing %v", s.Args, want)
		}
	}
	if slices.Contains(s.Args, "do it") {
		t.Error("the prompt must go on stdin, not argv")
	}
	if !slices.Contains(s.Env, "ANTHROPIC_BASE_URL=http://127.0.0.1:8317") || !slices.Contains(s.Env, "ANTHROPIC_AUTH_TOKEN=test-key") {
		t.Errorf("env %v", s.Env)
	}

	s, _ = BuildClaude(Turn{Perm: PermPlan, Resume: "5f0c2a8e-1111-4c2b-9a51-0d6c3b1f7a10", Name: "ignored", Review: &Review{}}, "/w", "x", Gateway{})
	if !has(s.Args, "--resume", "5f0c2a8e-1111-4c2b-9a51-0d6c3b1f7a10") || has(s.Args, "--name", "ignored") ||
		!has(s.Args, "--permission-mode", "plan") || !has(s.Args, "--allowedTools", reviewTools) || len(s.Env) != 0 {
		t.Errorf("resume review: %v %v", s.Args, s.Env)
	}

	if _, err := BuildClaude(Turn{Perm: PermBypass}, "/w", "x", Gateway{}); err == nil {
		t.Error("bypass without consent must fail")
	}
	s, _ = BuildClaude(Turn{Perm: PermBypass, Bypass: true}, "/w", "x", Gateway{})
	if !has(s.Args, "--dangerously-skip-permissions") {
		t.Errorf("bypass: %v", s.Args)
	}
	if _, err := BuildClaude(Turn{Perm: PermEdit, Resume: "--evil"}, "/w", "x", Gateway{}); err == nil {
		t.Error("a session id that looks like a flag must fail")
	}
	if _, err := BuildClaude(Turn{Perm: PermEdit, Model: "-x"}, "/w", "x", Gateway{}); err == nil {
		t.Error("a model that looks like a flag must fail")
	}
}

func TestBuildCodex(t *testing.T) {
	gw := Gateway{URL: "http://gw:8317/", Key: "k"}
	s, err := BuildCodex(Turn{Perm: PermEdit, Model: "gpt-5"}, "/w/app", "p", "/s/agent/1.last", gw)
	if err != nil {
		t.Fatal(err)
	}
	if s.Args[0] != "exec" || s.Args[len(s.Args)-1] != "-" || s.Stdin != "p" || s.LastFile != "/s/agent/1.last" {
		t.Fatalf("%v", s.Args)
	}
	for _, want := range [][]string{{"--json"}, {"--skip-git-repo-check"}, {"-C", "/w/app"}, {"--sandbox", "workspace-write"},
		{"-m", "gpt-5"}, {"-c", "model_provider=cpa"}, {"-o", "/s/agent/1.last"}} {
		if !has(s.Args, want...) {
			t.Errorf("args %v missing %v", s.Args, want)
		}
	}
	if !has(s.Args, "-c", `model_providers.cpa={name="CPA",base_url="http://gw:8317/v1",env_key="CPA_API_KEY",wire_api="responses",requires_openai_auth=false}`) {
		t.Errorf("gateway provider: %v", s.Args)
	}
	if !slices.Equal(s.Env, []string{"CPA_API_KEY=k"}) {
		t.Errorf("env %v", s.Env)
	}

	s, _ = BuildCodex(Turn{Perm: PermPlan, Resume: "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"}, "/w/app", "p", "", Gateway{})
	if !has(s.Args, "exec", "resume", "--json") || !has(s.Args, "-c", `sandbox_mode="read-only"`) || has(s.Args, "-C", "/w/app") ||
		!has(s.Args, "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", "-") || has(s.Args, "-c", "model_provider=cpa") {
		t.Errorf("resume: %v", s.Args)
	}
	s, _ = BuildCodex(Turn{Perm: PermBypass, Bypass: true}, "/w", "p", "", Gateway{})
	if !has(s.Args, "--dangerously-bypass-approvals-and-sandbox") || has(s.Args, "--sandbox") {
		t.Errorf("bypass: %v", s.Args)
	}
	if _, err := BuildCodex(Turn{Perm: PermBypass}, "/w", "p", "", Gateway{}); err == nil {
		t.Error("bypass without consent must fail")
	}
}

func TestResumeCommand(t *testing.T) {
	if got := ResumeCommand("claude", "/w/app", "abc"); got != "cd /w/app && claude --resume abc" {
		t.Error(got)
	}
	if got := ResumeCommand("codex", "/w/my app", "abc"); got != "cd '/w/my app' && codex resume abc" {
		t.Error(got)
	}
}

// ---------------------------------------------------------------- session id capture

func capture(t *testing.T, provider, file string) *Capture {
	c := &Capture{Provider: provider}
	var rendered []string
	for _, l := range strings.Split(fixture(t, file), "\n") {
		if r := c.Line(l); r != "" {
			rendered = append(rendered, r)
		}
	}
	if len(rendered) == 0 {
		t.Errorf("%s rendered nothing", file)
	}
	return c
}

func TestCaptureClaude(t *testing.T) {
	c := capture(t, "claude", "claude_stream.jsonl")
	if c.SessionID != "5f0c2a8e-1111-4c2b-9a51-0d6c3b1f7a10" || c.Final != "Fixed the off-by-one in pager.go; tests pass." || c.Err != "" {
		t.Fatalf("%+v", c)
	}
	c = &Capture{Provider: "claude"}
	if r := c.Line(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`); r != "→ Bash go test ./..." {
		t.Errorf("tool render: %q", r)
	}
	if r := c.Line("not json: warning"); r != "not json: warning" {
		t.Errorf("plain line: %q", r)
	}
	c.Line(`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"","session_id":"s"}`)
	if c.Err == "" || c.SessionID != "s" {
		t.Errorf("error result: %+v", c)
	}
}

func TestCaptureCodex(t *testing.T) {
	c := capture(t, "codex", "codex_exec.jsonl")
	if c.SessionID != "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b" || !strings.HasPrefix(c.Final, "## Verdict: ISSUES") {
		t.Fatalf("%+v", c)
	}
	c = capture(t, "codex", "codex_legacy.jsonl")
	if c.SessionID != "0b6f2c11-2222-4c2b-9a51-0d6c3b1f7a10" || c.Final != "## Verdict: CLEAN" {
		t.Fatalf("legacy: %+v", c)
	}
	c = &Capture{Provider: "codex"}
	c.Line(`{"type":"turn.failed","error":{"message":"usage limit reached"}}`)
	if c.Err != "usage limit reached" {
		t.Errorf("%+v", c)
	}
}

// ---------------------------------------------------------------- folders

func TestValidate(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	outside := filepath.Join(base, "secret")
	for _, d := range []string{filepath.Join(root, "app", "sub"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "sneaky")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "app"), filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRoots([]string{root, filepath.Join(base, "missing")})
	if len(r.Paths()) != 1 {
		t.Fatalf("roots %v", r.Paths())
	}
	realApp, _ := filepath.EvalSymlinks(filepath.Join(root, "app"))
	for dir, want := range map[string]error{
		filepath.Join(root, "app"):        nil,
		filepath.Join(root, "app", "sub"): nil,
		filepath.Join(base, "alias"):      nil, // a link from outside onto a folder inside is fine
		root:                              ErrOutside,
		outside:                           ErrOutside,
		filepath.Join(root, "sneaky"):     ErrOutside,
		root + "/app/../../secret":        ErrTraversal,
		root + "/app/../app":              ErrTraversal,
		"projects/app":                    ErrTraversal,
		"":                                ErrTraversal,
	} {
		got, err := r.Validate(dir)
		if want == nil {
			if err != nil || got != realApp && !strings.HasPrefix(got, realApp+"/") {
				t.Errorf("%s: %q %v", dir, got, err)
			}
			continue
		}
		if !errors.Is(err, want) {
			t.Errorf("%s: want %v, got %q %v", dir, want, got, err)
		}
	}
	if _, err := r.Validate(filepath.Join(root, "nope")); err == nil {
		t.Error("missing folder accepted")
	}
	if _, err := r.Validate(filepath.Join(root, "file")); err == nil {
		t.Error("a file accepted as a folder")
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func TestScanAndWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "app")
	_ = os.MkdirAll(filepath.Join(root, "group", "lib"), 0o755)
	_ = os.MkdirAll(repo, 0o755)
	gitInit(t, repo)
	gitInit(t, filepath.Join(root, "group", "lib"))
	r := NewRoots([]string{root})
	ctx := context.Background()
	if _, err := MakeWorktree(ctx, r, repo, "main", "Bad Name"); err == nil {
		t.Error("bad slug accepted")
	}
	if _, err := MakeWorktree(ctx, r, repo, "--upload-pack=x", "ok"); err == nil {
		t.Error("flag-like base accepted")
	}
	wt, err := MakeWorktree(ctx, r, repo, "main", "fix-pager")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(wt) != "app-wt-fix-pager" {
		t.Errorf("worktree at %s", wt)
	}
	if _, err := MakeWorktree(ctx, r, wt, "main", "fix-pager"); err == nil {
		t.Error("existing worktree made twice")
	}
	var names []string
	for _, f := range r.Scan(100) {
		names = append(names, filepath.Base(f.Path)+"@"+f.Branch)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"app-wt-fix-pager@fix-pager", "app@main", "lib@main"}) {
		t.Errorf("scan: %v", names)
	}
	if b := DefaultBase(ctx, repo); b != "main" {
		t.Errorf("default base %q", b)
	}
}

func TestExpandVars(t *testing.T) {
	out, missing := ExpandVars("Fix {{ISSUE}} in {{ REPO }}; {{ISSUE}} again; {{OWNER}}", map[string]string{"ISSUE": "#12", "REPO": "app"})
	if out != "Fix #12 in app; #12 again; {{OWNER}}" || !slices.Equal(missing, []string{"OWNER"}) {
		t.Errorf("%q %v", out, missing)
	}
	if v := PromptVars("{{A}} {{B}} {{A}}"); !slices.Equal(v, []string{"A", "B"}) {
		t.Error(v)
	}
	if m := ParseVars("A=1\n B = two words \nnope"); m["A"] != "1" || m["B"] != "two words" || len(m) != 2 {
		t.Error(m)
	}
}

func TestConfig(t *testing.T) {
	kv := yamlScalars([]byte("paused: false\nwork_root: ~/work   # repos\ngateway:\n  enabled: true\n  url: http://127.0.0.1:8317\n  env_file: \"~/.config/x.env\"\nroles:\n  implementer: claude\npeople: {}\n"))
	if kv["work_root"] != "~/work" || kv["gateway.enabled"] != "true" || kv["gateway.url"] != "http://127.0.0.1:8317" ||
		kv["gateway.env_file"] != "~/.config/x.env" || kv["roles.implementer"] != "claude" {
		t.Errorf("%v", kv)
	}
	f := filepath.Join(t.TempDir(), "cpa.env")
	_ = os.WriteFile(f, []byte("# keys\nexport CPA_API_KEY=\"abc\"\nCPA_MGMT_KEY=m\n"), 0o600)
	if v := envValue(f, "CPA_API_KEY"); v != "abc" {
		t.Error(v)
	}
	if g := (Config{GatewayEnabled: false, GatewayURL: "http://x"}).ResolveGateway(context.Background()); g.On() {
		t.Error("disabled gateway used")
	}
}

// ---------------------------------------------------------------- the whole loop, with a fake runner

// fakeRunner replays a fixture per provider. A "block" output waits until Stop.
type fakeRunner struct {
	mu    sync.Mutex
	specs []Spec
	out   map[string]string
	exit  int
}

func (f *fakeRunner) Start(_ context.Context, s Spec) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, s)
	out := f.out[s.Bin]
	p := &fakeProc{exit: f.exit, stopped: make(chan struct{})}
	if out == "block" {
		pr, pw := io.Pipe()
		p.r, p.w, p.block = pr, pw, true
		return p, nil
	}
	p.r = io.NopCloser(strings.NewReader(out))
	return p, nil
}

func (f *fakeRunner) last() Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[len(f.specs)-1]
}

type fakeProc struct {
	r       io.ReadCloser
	w       *io.PipeWriter
	block   bool
	exit    int
	once    sync.Once
	stopped chan struct{}
}

func (p *fakeProc) PID() int          { return 4242 }
func (p *fakeProc) Output() io.Reader { return p.r }
func (p *fakeProc) Close()            { _ = p.r.Close() }
func (p *fakeProc) Stop() {
	p.once.Do(func() {
		close(p.stopped)
		if p.w != nil {
			_ = p.w.Close()
		}
	})
}
func (p *fakeProc) Wait() (int, error) {
	if p.block {
		<-p.stopped
		return 143, nil
	}
	return p.exit, nil
}

type rig struct {
	st   *store.Store
	ctl  *Control
	hub  *LocalHub
	ex   *Executor
	run  *fakeRunner
	repo string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root := t.TempDir()
	repo := filepath.Join(root, "app")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	repo, _ = filepath.EvalSymlinks(repo)
	run := &fakeRunner{out: map[string]string{"claude": fixture(t, "claude_stream.jsonl"), "codex": fixture(t, "codex_exec.jsonl")}}
	hub := &LocalHub{S: st}
	ex := &Executor{Host: "studio", Version: "test", Hub: hub, Roots: NewRoots([]string{root}), StateDir: t.TempDir(), Runner: run,
		Providers: []string{"claude", "codex"}}
	ex.init()
	if err := hub.Inventory(context.Background(), ex.inventory()); err != nil {
		t.Fatal(err)
	}
	return &rig{st: st, ctl: &Control{S: st}, hub: hub, ex: ex, run: run, repo: repo}
}

// cycle polls once and waits for the turns it started.
func (r *rig) cycle(t *testing.T) {
	t.Helper()
	if err := r.ex.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.ex.Wait()
}

func (r *rig) session(t *testing.T, id int64) store.AgentSession {
	t.Helper()
	a, err := r.st.AgentSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestReviewSendBackFlow(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)

	if _, err := r.ctl.Start(ctx, StartReq{Host: "studio", Cwd: r.repo, Provider: "claude", Perm: PermBypass, Prompt: "x"}); err == nil {
		t.Fatal("bypass without the box ticked was accepted")
	}
	if _, err := r.ctl.Start(ctx, StartReq{Host: "studio", Cwd: "/etc", Provider: "claude", Prompt: "x"}); err == nil {
		t.Fatal("a folder outside the roots was accepted")
	}
	if _, err := r.ctl.Start(ctx, StartReq{Host: "studio", Cwd: r.repo, Provider: "gemini", Prompt: "x"}); err == nil {
		t.Fatal("a provider the host doesn't have was accepted")
	}
	if _, err := r.ctl.Start(ctx, StartReq{Host: "laptop", Cwd: r.repo, Provider: "claude", Prompt: "x"}); err == nil {
		t.Fatal("a host with no agent was accepted")
	}
	id, err := r.ctl.Start(ctx, StartReq{Host: "studio", Cwd: r.repo, Provider: "claude", Prompt: "Fix the pager in {{REPO}}"})
	if err != nil {
		t.Fatal(err)
	}
	if a := r.session(t, id); a.Status != "queued" || a.Perm != PermEdit {
		t.Fatalf("queued: %+v", a)
	}
	r.cycle(t)
	a := r.session(t, id)
	if a.Status != "idle" || a.ProviderSession != "5f0c2a8e-1111-4c2b-9a51-0d6c3b1f7a10" || a.LastResult != "Fixed the off-by-one in pager.go; tests pass." {
		t.Fatalf("after first turn: %+v", a)
	}
	if s := r.run.last(); s.Stdin != "Fix the pager in app" || s.Dir != r.repo {
		t.Fatalf("first turn spec: %+v", s)
	}
	if keys := r.ex.RunningKeys(); len(keys) != 0 {
		t.Fatalf("still running: %v", keys)
	}

	// Codex reviews the same folder, read-only.
	rid, err := r.ctl.Review(ctx, id, "main")
	if err != nil {
		t.Fatal(err)
	}
	r.cycle(t)
	rv := r.session(t, rid)
	if rv.Provider != "codex" || rv.ReviewOf != id || rv.Status != "idle" || !strings.Contains(rv.LastResult, "pager.go:42") ||
		rv.ProviderSession != "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b" {
		t.Fatalf("review: %+v", rv)
	}
	if s := r.run.last(); s.Bin != "codex" || !has(s.Args, "--sandbox", "read-only") || !strings.Contains(s.Stdin, "git diff main...HEAD") || s.Dir != r.repo {
		t.Fatalf("review spec: %+v", s)
	}

	// The findings go back to the claude session as a resume turn.
	if _, err := r.ctl.SendBack(ctx, id); err == nil {
		t.Fatal("send-back from a non-review accepted")
	}
	if _, err := r.ctl.SendBack(ctx, rid); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ctl.SendBack(ctx, rid); err == nil {
		t.Fatal("a second send-back while the first is queued was accepted")
	}
	r.cycle(t)
	s := r.run.last()
	if s.Bin != "claude" || !has(s.Args, "--resume", "5f0c2a8e-1111-4c2b-9a51-0d6c3b1f7a10") ||
		!strings.HasPrefix(s.Stdin, "Address these review findings") || !strings.Contains(s.Stdin, "pager.go:42") {
		t.Fatalf("send-back spec: %+v", s)
	}
	if a := r.session(t, id); a.Status != "idle" || a.Turns != 2 {
		t.Fatalf("after send-back: %+v", a)
	}
	cmds, _ := r.st.Commands(ctx, 0, 50)
	if len(cmds) != 3 {
		t.Fatalf("commands: %d", len(cmds))
	}
	for _, c := range cmds {
		if c.Status != "done" || c.FinishedAt == "" {
			t.Errorf("command %d: %s", c.ID, c.Status)
		}
	}
	// The turn logs land where the shipper picks up worker logs.
	logs, _ := filepath.Glob(filepath.Join(r.ex.StateDir, "logs", "agent-*.log"))
	if len(logs) < 2 { // two turns of one session in the same second share a log
		t.Errorf("logs: %v", logs)
	}
}

func TestScratchChat(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	id, err := r.ctl.Start(ctx, StartReq{Host: "studio", Provider: "claude", Prompt: "what is a monad?"})
	if err != nil {
		t.Fatal(err)
	}
	if a := r.session(t, id); a.Perm != PermPlan || !a.Scratch || a.Cwd != "" {
		t.Fatalf("%+v", a)
	}
	if err := r.ctl.FollowUp(ctx, id, "more"); err == nil {
		t.Fatal("follow-up before the first turn accepted")
	}
	r.cycle(t)
	a := r.session(t, id)
	if a.Status != "idle" || !strings.HasSuffix(a.Cwd, filepath.Join("chats", "1")) {
		t.Fatalf("%+v", a)
	}
	if err := r.ctl.FollowUp(ctx, id, "and a functor?"); err != nil {
		t.Fatal(err)
	}
	r.cycle(t)
	if a := r.session(t, id); a.Status != "idle" || a.Error != "" {
		t.Fatalf("follow-up in scratch dir: %+v", a)
	}
	if s := r.run.last(); s.Dir != a.Cwd || !has(s.Args, "--permission-mode", "plan") {
		t.Fatalf("%+v", s)
	}
}

func TestStopAndFailures(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.run.out["claude"] = "block"
	id, err := r.ctl.Start(ctx, StartReq{Host: "studio", Cwd: r.repo, Provider: "claude", Prompt: "long job"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ex.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.session(t, id).Status != "running" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if keys := r.ex.RunningKeys(); !slices.Equal(keys, []string{"agent-1"}) {
		t.Fatalf("running keys %v", keys)
	}
	if err := r.ctl.FollowUp(ctx, id, "more"); err == nil {
		t.Fatal("follow-up while running accepted")
	}
	if err := r.ctl.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	r.cycle(t)
	if a := r.session(t, id); a.Status != "stopped" || a.PID != 0 {
		t.Fatalf("after stop: %+v", a)
	}

	// A failing turn reports its error.
	r.run.out["claude"] = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"","session_id":"5f0c2a8e-1111-4c2b-9a51-0d6c3b1f7a10"}` + "\n"
	r.run.exit = 1
	id2, _ := r.ctl.Start(ctx, StartReq{Host: "studio", Cwd: r.repo, Provider: "claude", Prompt: "x"})
	r.cycle(t)
	if a := r.session(t, id2); a.Status != "failed" || !strings.Contains(a.Error, "error_during_execution") {
		t.Fatalf("failed turn: %+v", a)
	}

	// Stopping a turn no host has claimed just cancels it.
	id3, _ := r.ctl.Start(ctx, StartReq{Host: "studio", Cwd: r.repo, Provider: "codex", Prompt: "x"})
	if err := r.ctl.Stop(ctx, id3); err != nil {
		t.Fatal(err)
	}
	if a := r.session(t, id3); a.Status != "stopped" {
		t.Fatalf("cancelled: %+v", a)
	}
	if cs, _ := r.st.ClaimCommands(ctx, "studio"); len(cs) != 0 {
		t.Fatalf("cancelled command still claimable: %v", cs)
	}
}

func TestLostCommandIsFailed(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	id, _ := r.ctl.Start(ctx, StartReq{Host: "studio", Cwd: r.repo, Provider: "claude", Prompt: "x"})
	if cs, _ := r.hub.Poll(ctx, "studio", "v", nil); len(cs) != 1 {
		t.Fatal("not claimed")
	}
	// The executor restarts and doesn't list the command; once it's old enough, the hub fails it.
	r.hub.LostAfter = time.Nanosecond
	time.Sleep(5 * time.Millisecond)
	if _, err := r.hub.Poll(ctx, "studio", "v", nil); err != nil {
		t.Fatal(err)
	}
	if a := r.session(t, id); a.Status != "failed" || !strings.Contains(a.Error, "restarted") {
		t.Fatalf("%+v", a)
	}
}

func TestExecutorRefusesOutsideRoots(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	// Queued straight into the store, past the hub's checks: the executor must still refuse.
	id, _, err := r.st.CreateAgentSession(ctx, store.AgentSession{Host: "studio", Cwd: "/etc", Provider: "claude", Perm: PermEdit},
		&store.Command{Host: "studio", Kind: KindTurn, Payload: payload(Turn{Provider: "claude", Cwd: "/etc", Perm: PermEdit, Prompt: "x"})})
	if err != nil {
		t.Fatal(err)
	}
	id2, _, _ := r.st.CreateAgentSession(ctx, store.AgentSession{Host: "studio", Cwd: r.repo, Provider: "claude", Perm: PermBypass},
		&store.Command{Host: "studio", Kind: KindTurn, Payload: payload(Turn{Provider: "claude", Cwd: r.repo, Perm: PermBypass, Prompt: "x"})})
	r.cycle(t)
	if a := r.session(t, id); a.Status != "failed" || !strings.Contains(a.Error, "not under the allowed roots") {
		t.Fatalf("%+v", a)
	}
	if a := r.session(t, id2); a.Status != "failed" || !strings.Contains(a.Error, "consent") {
		t.Fatalf("%+v", a)
	}
	if len(r.run.specs) != 0 {
		t.Fatalf("started %d processes", len(r.run.specs))
	}
}

func TestGemini(t *testing.T) {
	s, err := BuildGemini(Turn{Perm: PermEdit, Model: "gemini-2.5-pro", Resume: "c2d4e6f8-3333-4c2b-9a51-0d6c3b1f7a10"}, "/w", "-starts with a dash")
	if err != nil {
		t.Fatal(err)
	}
	if s.Bin != "gemini" || s.Args[0] != "--prompt=-starts with a dash" || !has(s.Args, "--output-format", "stream-json") ||
		!has(s.Args, "--approval-mode", "auto_edit") || !has(s.Args, "--resume", "c2d4e6f8-3333-4c2b-9a51-0d6c3b1f7a10") {
		t.Fatalf("%v", s.Args)
	}
	if got := shownArgs(s.Args)[0]; strings.Contains(got, "dash") && len(got) > 80 {
		t.Error(got)
	}
	c := capture(t, "gemini", "gemini_stream.jsonl")
	if c.SessionID != "c2d4e6f8-3333-4c2b-9a51-0d6c3b1f7a10" || c.Final != "## Verdict: CLEAN" || c.Err != "" {
		t.Fatalf("%+v", c)
	}
	if Other("gemini") != "claude" || Other("claude") != "codex" || Other("codex") != "claude" {
		t.Error("reviewer pairing")
	}
}
