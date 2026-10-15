package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Control is the hub side: it checks what the pages ask for and queues commands. The host's executor checks
// everything again (folders against its roots, after resolving symlinks) before it runs anything.
type Control struct {
	S   *store.Store
	Log *log.Logger
}

// InputError is a problem with what was asked; pages show it as is.
type InputError struct{ Msg string }

func (e InputError) Error() string { return e.Msg }

func bad(f string, a ...any) error { return InputError{fmt.Sprintf(f, a...)} }

func (c *Control) logf(f string, a ...any) {
	if c.Log != nil {
		c.Log.Printf(f, a...)
	}
}

const maxPrompt = 256 << 10

type StartReq struct {
	Host, Cwd, Provider, Model, Perm string
	Bypass                           bool // the person ticked "allow bypass" for this session
	WorktreeBase, WorktreeSlug       string
	Prompt                           string
	Vars                             map[string]string
}

var varRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// PromptVars lists the {{VARS}} in a prompt, in order of first use.
func PromptVars(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range varRe.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// ExpandVars fills {{VARS}}; it returns the names it had no value for.
func ExpandVars(text string, vars map[string]string) (string, []string) {
	var missing []string
	seen := map[string]bool{}
	out := varRe.ReplaceAllStringFunc(text, func(m string) string {
		k := varRe.FindStringSubmatch(m)[1]
		if v, ok := vars[k]; ok && v != "" {
			return v
		}
		if !seen[k] {
			seen[k] = true
			missing = append(missing, k)
		}
		return m
	})
	return out, missing
}

// ParseVars reads KEY=value lines.
func ParseVars(s string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func cleanAbs(p string) (string, bool) {
	if p == "" || !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
		return "", false
	}
	for _, el := range strings.Split(p, "/") {
		if el == ".." {
			return "", false
		}
	}
	return filepath.Clean(p), true
}

// liveHost is the host's agent, when it has polled recently.
func (c *Control) liveHost(ctx context.Context, host string) (store.AgentHost, error) {
	h, err := c.S.AgentHost(ctx, host)
	if errors.Is(err, store.ErrNotFound) {
		return h, bad("%s has no session agent (run `hub serve` or `hub ship` there)", host)
	}
	if err != nil {
		return h, err
	}
	if !h.Live() {
		return h, bad("the session agent on %s is offline (last seen %s)", host, h.LastPoll)
	}
	return h, nil
}

// HostLive: the host has a session agent that polled recently.
func (c *Control) HostLive(ctx context.Context, host string) bool {
	h, err := c.S.AgentHost(ctx, host)
	return err == nil && h.Live()
}

func title(prompt string) string {
	t := strings.TrimSpace(prompt)
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[:i]
	}
	if len(t) > 100 {
		t = t[:100] + "…"
	}
	return t
}

func payload(t Turn) json.RawMessage {
	b, _ := json.Marshal(t)
	return b
}

// Start creates an agent session and queues its first turn.
func (c *Control) Start(ctx context.Context, r StartReq) (int64, error) {
	if !KnownProvider(r.Provider) {
		return 0, bad("pick Claude, Codex or Gemini")
	}
	scratch := strings.TrimSpace(r.Cwd) == ""
	if scratch && r.Perm == "" {
		r.Perm = PermPlan // a chat with no project folder reads and talks; it doesn't edit
	}
	perm, err := NormPerm(r.Perm)
	if err != nil {
		return 0, bad("%v", err)
	}
	if perm == PermBypass && !r.Bypass {
		return 0, bad("bypass mode needs the \"allow bypass for this session\" box ticked")
	}
	if r.Model != "" && !modelRe.MatchString(r.Model) {
		return 0, bad("bad model name")
	}
	h, err := c.liveHost(ctx, r.Host)
	if err != nil {
		return 0, err
	}
	if len(h.Providers) > 0 && !slices.Contains(h.Providers, r.Provider) {
		return 0, bad("%s isn't installed on %s", ProviderName(r.Provider), r.Host)
	}
	cwd := ""
	if !scratch {
		var ok bool
		if cwd, ok = cleanAbs(r.Cwd); !ok {
			return 0, bad("the folder must be an absolute path without '..'")
		}
		if !knownFolder(h, cwd) {
			return 0, bad("%s is not a folder under %s's allowed roots", cwd, r.Host)
		}
	}
	var wt *Worktree
	if r.WorktreeSlug != "" && scratch {
		return 0, bad("pick a repo to make a worktree from")
	}
	if r.WorktreeSlug != "" {
		if !ValidSlug(r.WorktreeSlug) {
			return 0, bad("worktree name: lowercase letters, digits, '.', '_' and '-'")
		}
		if !ValidRef(r.WorktreeBase) {
			return 0, bad("pick the base branch for the new worktree")
		}
		wt = &Worktree{Base: r.WorktreeBase, Slug: r.WorktreeSlug}
	}
	vars := map[string]string{"HOST": r.Host, "BASE": r.WorktreeBase}
	if cwd != "" {
		vars["CWD"], vars["REPO"] = cwd, filepath.Base(cwd)
	}
	for k, v := range r.Vars {
		vars[k] = v
	}
	prompt, missing := ExpandVars(r.Prompt, vars)
	if len(missing) > 0 {
		return 0, bad("fill in the prompt's variables: %s (as NAME=value lines)", strings.Join(missing, ", "))
	}
	if strings.TrimSpace(prompt) == "" {
		return 0, bad("write a prompt")
	}
	if len(prompt) > maxPrompt {
		return 0, bad("the prompt is over %d KB", maxPrompt>>10)
	}
	t := Turn{Provider: r.Provider, Cwd: cwd, Model: r.Model, Perm: perm, Bypass: perm == PermBypass && r.Bypass, Prompt: prompt, Worktree: wt, Scratch: scratch}
	a := store.AgentSession{Host: r.Host, Cwd: cwd, Provider: r.Provider, Model: r.Model, Perm: perm, BypassOK: t.Bypass, Title: title(prompt),
		Scratch: scratch}
	id, cid, err := c.S.CreateAgentSession(ctx, a, &store.Command{Host: r.Host, Kind: KindTurn, Payload: payload(t)})
	if err != nil {
		return 0, err
	}
	c.logf("agent: queued command %d: start %s session %d on %s in %s (%s)", cid, r.Provider, id, r.Host, cwd, perm)
	return id, nil
}

// knownFolder: a folder the host listed, or one inside a listed folder or root (the executor resolves it).
func knownFolder(h store.AgentHost, cwd string) bool {
	for _, f := range h.Folders {
		if cwd == f.Path || strings.HasPrefix(cwd, f.Path+"/") {
			return true
		}
	}
	for _, r := range h.Roots {
		if strings.HasPrefix(cwd, strings.TrimRight(r, "/")+"/") {
			return true
		}
	}
	return false
}

// FollowUp queues a resume turn.
func (c *Control) FollowUp(ctx context.Context, id int64, prompt string) error {
	a, err := c.S.AgentSession(ctx, id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(prompt) == "" {
		return bad("write a prompt")
	}
	if len(prompt) > maxPrompt {
		return bad("the prompt is over %d KB", maxPrompt>>10)
	}
	if a.ProviderSession == "" {
		return bad("this session has no %s session id yet; wait for its first turn to start", a.Provider)
	}
	if _, err := c.liveHost(ctx, a.Host); err != nil {
		return err
	}
	t := Turn{Provider: a.Provider, Cwd: a.Cwd, Model: a.Model, Perm: a.Perm, Bypass: a.Perm == PermBypass && a.BypassOK,
		Prompt: prompt, Resume: a.ProviderSession, Scratch: a.Scratch}
	if a.Cwd == "" {
		return bad("this session's folder isn't known yet; wait for its first turn to start")
	}
	if a.ReviewOf > 0 {
		t.Perm, t.Bypass, t.Review = PermPlan, false, &Review{}
	}
	cid, err := c.S.QueueTurn(ctx, store.Command{Host: a.Host, Kind: KindTurn, AgentID: id, Payload: payload(t)})
	if errors.Is(err, store.ErrBusy) {
		return bad("a turn is already queued or running; stop it or wait")
	}
	if err != nil {
		return err
	}
	c.logf("agent: queued command %d: follow-up on session %d (%s on %s)", cid, id, a.Provider, a.Host)
	return nil
}

// Stop cancels a queued turn, or asks the host to stop the running one.
func (c *Control) Stop(ctx context.Context, id int64) error {
	a, err := c.S.AgentSession(ctx, id)
	if err != nil {
		return err
	}
	if n, err := c.S.CancelQueued(ctx, id); err != nil {
		return err
	} else if n > 0 {
		c.logf("agent: cancelled %d queued turn(s) of session %d", n, id)
		msg := "cancelled before it started"
		zero := 0
		return c.S.UpdateAgentSession(ctx, id, store.AgentUpdate{Status: "stopped", PID: &zero, Error: &msg})
	}
	if a.Status != "running" {
		// A turn claimed but not reported yet still gets the stop: the executor ignores it if nothing runs.
		fl, _ := c.S.InFlight(ctx, a.Host)
		busy := false
		for _, x := range fl {
			busy = busy || (x.AgentID == id && x.Kind == KindTurn)
		}
		if !busy {
			return bad("nothing is running in this session")
		}
	}
	b, _ := json.Marshal(Stop{})
	cid, err := c.S.QueueCommand(ctx, store.Command{Host: a.Host, Kind: KindStop, AgentID: id, Payload: b})
	if err != nil {
		return err
	}
	c.logf("agent: queued command %d: stop session %d on %s", cid, id, a.Host)
	return nil
}

// Other is the provider that reviews this one's work: Codex reviews Claude, Claude reviews the rest.
func Other(provider string) string {
	if provider == "claude" {
		return "codex"
	}
	return "claude"
}

// Review queues a read-only review of the session's folder by the other provider, as its own session.
func (c *Control) Review(ctx context.Context, id int64, base string) (int64, error) {
	a, err := c.S.AgentSession(ctx, id)
	if err != nil {
		return 0, err
	}
	if a.Cwd == "" || (a.Status == "queued" && a.ProviderSession == "") {
		return 0, bad("wait for the session's first turn to start (its folder may still change)")
	}
	if base != "" && !ValidRef(base) {
		return 0, bad("bad base branch %q", base)
	}
	if _, err := c.liveHost(ctx, a.Host); err != nil {
		return 0, err
	}
	other := Other(a.Provider)
	t := Turn{Provider: other, Cwd: a.Cwd, Perm: PermPlan, Review: &Review{Base: base}, Scratch: a.Scratch}
	what := a.Title
	if what == "" {
		what = filepath.Base(a.Cwd)
	}
	r := store.AgentSession{Host: a.Host, Cwd: a.Cwd, Provider: other, Perm: PermPlan, ReviewOf: id, Scratch: a.Scratch,
		Title: title(fmt.Sprintf("Review of #%d: %s", id, what))}
	rid, cid, err := c.S.CreateAgentSession(ctx, r, &store.Command{Host: a.Host, Kind: KindTurn, Payload: payload(t)})
	if err != nil {
		return 0, err
	}
	c.logf("agent: queued command %d: %s reviews session %d as session %d (base %q)", cid, other, id, rid, base)
	return rid, nil
}

// SendBack queues a turn on the reviewed session with the review's findings.
func (c *Control) SendBack(ctx context.Context, reviewID int64) (int64, error) {
	r, err := c.S.AgentSession(ctx, reviewID)
	if err != nil {
		return 0, err
	}
	if r.ReviewOf == 0 {
		return 0, bad("session %d is not a review", reviewID)
	}
	if r.Busy() || strings.TrimSpace(r.LastResult) == "" {
		return 0, bad("the review has no findings yet")
	}
	if err := c.FollowUp(ctx, r.ReviewOf, SendBackPrompt(r.LastResult)); err != nil {
		return 0, err
	}
	c.logf("agent: review %d sent back to session %d", reviewID, r.ReviewOf)
	return r.ReviewOf, nil
}

// Adopt gives an ingested session (started anywhere) an agent session, so the hub can continue it.
func (c *Control) Adopt(ctx context.Context, s store.Session, perm string, bypass bool) (int64, error) {
	if !KnownProvider(s.Provider) {
		return 0, bad("only Claude, Codex and Gemini sessions can be continued")
	}
	if s.UUID == "" || s.ParentUUID != "" {
		return 0, bad("this transcript can't be resumed (no session id, or a subagent)")
	}
	if a, err := c.S.AgentSessionByProvider(ctx, s.Host, s.UUID); err == nil {
		return a.ID, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	cwd, ok := cleanAbs(s.Cwd)
	if !ok {
		return 0, bad("the session's folder is unknown")
	}
	p, err := NormPerm(perm)
	if err != nil {
		return 0, bad("%v", err)
	}
	if p == PermBypass && !bypass {
		return 0, bad("bypass mode needs the \"allow bypass for this session\" box ticked")
	}
	if _, err := c.liveHost(ctx, s.Host); err != nil {
		return 0, err
	}
	a := store.AgentSession{Host: s.Host, Cwd: cwd, Provider: s.Provider, Model: "", Perm: p, BypassOK: p == PermBypass && bypass,
		ProviderSession: s.UUID, Status: "idle", Adopted: true, Title: title(s.DisplayTitle())}
	id, _, err := c.S.CreateAgentSession(ctx, a, nil)
	if err != nil {
		return 0, err
	}
	c.logf("agent: adopted %s session %s on %s as session %d", s.Provider, s.UUID, s.Host, id)
	return id, nil
}

// Folders lists every live host's folders, hosts sorted.
func (c *Control) Folders(ctx context.Context) ([]store.AgentHost, error) {
	hs, err := c.S.AgentHosts(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].Host < hs[j].Host })
	return hs, nil
}
