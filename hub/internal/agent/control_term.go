package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// TermReq is a new terminal from the hub's form.
type TermReq struct {
	Host, Provider, Cwd, Model, Prompt string
	WorktreeBase, WorktreeSlug         string
}

// OpenTerminal records a terminal and queues its term_open. The folder is only checked for shape here; the
// host resolves it (and refuses one outside the home folder).
func (c *Control) OpenTerminal(ctx context.Context, r TermReq) (int64, error) {
	if !KnownTermProvider(r.Provider) {
		return 0, bad("pick Claude Code, Codex, Gemini or a shell")
	}
	h, err := c.liveHost(ctx, r.Host)
	if err != nil {
		return 0, err
	}
	if r.Provider != "shell" && len(h.Providers) > 0 && !contains(h.Providers, r.Provider) {
		return 0, bad("%s isn't installed on %s", ProviderName(r.Provider), r.Host)
	}
	if r.Model != "" && !modelRe.MatchString(r.Model) {
		return 0, bad("bad model name %q", r.Model)
	}
	cwd := strings.TrimSpace(r.Cwd)
	if cwd != "" {
		rest, tilde := strings.CutPrefix(cwd, "~")
		if tilde && rest != "" && !strings.HasPrefix(rest, "/") {
			return 0, bad("write the folder as an absolute path or ~/...")
		}
		p := cwd
		if tilde {
			p = "/home" + rest // shape check only; the host expands ~
		}
		if _, ok := cleanAbs(p); !ok {
			return 0, bad("write the folder as an absolute path or ~/..., without '..'")
		}
		cwd = strings.TrimRight(cwd, "/")
		if cwd == "" || cwd == "~" {
			cwd = "~"
		}
	}
	o := TermOpen{Provider: r.Provider, Cwd: cwd, Model: r.Model, Prompt: strings.TrimSpace(r.Prompt)}
	if r.WorktreeSlug != "" {
		if cwd == "" {
			return 0, bad("pick the repo to make the worktree from")
		}
		base := r.WorktreeBase
		if base == "" {
			base = "origin/main"
		}
		if !ValidSlug(r.WorktreeSlug) || !ValidRef(base) {
			return 0, bad("bad worktree name or base branch")
		}
		o.Worktree = &Worktree{Base: base, Slug: r.WorktreeSlug}
	}
	t := title(o.Prompt)
	if t == "" {
		switch {
		case o.Worktree != nil:
			t = o.Worktree.Slug
		case cwd != "":
			t = filepath.Base(cwd)
		default:
			t = "scratch"
		}
	}
	b, _ := json.Marshal(o)
	id, err := c.S.CreateTerminal(ctx, store.Terminal{Host: r.Host, Provider: r.Provider, Cwd: cwd, Model: r.Model, Title: t},
		store.Command{Host: r.Host, Kind: KindTermOpen, Payload: b})
	if err != nil {
		return 0, err
	}
	c.logf("agent: terminal %d: %s on %s in %q", id, r.Provider, r.Host, cwd)
	return id, nil
}

// KillTerminal queues a term_kill. With the host's agent offline the terminal is only marked ended: the
// tmux session, if it still exists, is listed again (and revived) by the next inventory.
func (c *Control) KillTerminal(ctx context.Context, id int64) error {
	t, err := c.S.Terminal(ctx, id)
	if err != nil {
		return err
	}
	if !t.Open() {
		return nil
	}
	opened, err := c.S.CancelQueuedTerm(ctx, id)
	if err != nil {
		return err
	}
	if opened { // no host had it yet: nothing to kill
		return c.S.SetTerminal(ctx, id, "ended", "", "")
	}
	if !c.HostLive(ctx, t.Host) {
		return bad("the agent on %s is offline; end it there with: tmux -L %s kill-session -t %s", t.Host, tmuxSocket, TmuxName(id))
	}
	_, err = c.S.QueueCommand(ctx, store.Command{Host: t.Host, Kind: KindTermKill, AgentID: id})
	return err
}

// Attach queues a term_attach for the bridge the hub is holding open under token.
func (c *Control) Attach(ctx context.Context, t store.Terminal, token string, cols, rows int) (int64, error) {
	b, _ := json.Marshal(TermAttach{Token: token, Cols: cols, Rows: rows})
	return c.S.QueueCommand(ctx, store.Command{Host: t.Host, Kind: KindTermAttach, AgentID: t.ID, Payload: b})
}

// ReviewTerminal starts a read-only review session by provider of the terminal's folder.
func (c *Control) ReviewTerminal(ctx context.Context, id int64, provider string) (int64, error) {
	t, err := c.S.Terminal(ctx, id)
	if err != nil {
		return 0, err
	}
	if t.Status == "starting" || !filepath.IsAbs(t.Cwd) {
		return 0, bad("wait for the terminal to start")
	}
	if provider != "claude" && provider != "codex" {
		return 0, bad("reviews run on Claude or Codex")
	}
	if _, err := c.liveHost(ctx, t.Host); err != nil {
		return 0, err
	}
	tr := Turn{Provider: provider, Cwd: t.Cwd, Perm: PermPlan, Review: &Review{}, InHome: true}
	a := store.AgentSession{Host: t.Host, Cwd: t.Cwd, Provider: provider, Perm: PermPlan,
		Title: title(fmt.Sprintf("Review of terminal #%d: %s", id, t.Title))}
	rid, cid, err := c.S.CreateAgentSession(ctx, a, &store.Command{Host: t.Host, Kind: KindTurn, Payload: payload(tr)})
	if err != nil {
		return 0, err
	}
	c.logf("agent: queued command %d: %s reviews terminal %d as session %d", cid, provider, id, rid)
	return rid, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
