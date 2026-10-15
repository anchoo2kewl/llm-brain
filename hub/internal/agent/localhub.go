package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/parse"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// LocalHub is the queue itself, on the store: what `hub serve`'s own executor talks to, and what the
// /api/agent endpoints call for remote ones.
type LocalHub struct {
	S   *store.Store
	Log *log.Logger
	// LostAfter: a claimed command its executor no longer lists is failed after this long.
	LostAfter time.Duration
}

func (h *LocalHub) logf(f string, a ...any) {
	if h.Log != nil {
		h.Log.Printf(f, a...)
	}
}

func (h *LocalHub) Poll(ctx context.Context, host, version string, running []int64) ([]store.Command, error) {
	if host == "" {
		return nil, fmt.Errorf("poll needs host")
	}
	if err := h.S.TouchAgentHost(ctx, host, version); err != nil {
		return nil, err
	}
	if err := h.reconcile(ctx, host, running); err != nil {
		return nil, err
	}
	cs, err := h.S.ClaimCommands(ctx, host)
	for _, c := range cs {
		h.logf("agent: %s claimed command %d (%s, session %d)", host, c.ID, c.Kind, c.AgentID)
	}
	return cs, err
}

// reconcile fails the host's in-flight commands its executor no longer has (it restarted mid-turn).
func (h *LocalHub) reconcile(ctx context.Context, host string, running []int64) error {
	lost := h.LostAfter
	if lost <= 0 {
		lost = time.Minute
	}
	have := map[int64]bool{}
	for _, id := range running {
		have[id] = true
	}
	fl, err := h.S.InFlight(ctx, host)
	if err != nil {
		return err
	}
	cutoff := store.T(time.Now().Add(-lost))
	for _, c := range fl {
		if have[c.ID] || c.ClaimedAt > cutoff {
			continue
		}
		if err := h.Report(ctx, c.ID, Report{Status: "failed", Error: "the agent on " + host + " no longer has this turn (it restarted?)"}); err != nil {
			return err
		}
	}
	return nil
}

func (h *LocalHub) Report(ctx context.Context, id int64, r Report) error {
	switch r.Status {
	case "running", "done", "failed", "stopped":
	default:
		return fmt.Errorf("bad status %q", r.Status)
	}
	c, err := h.S.Command(ctx, id)
	if err != nil {
		return err
	}
	if c.Status == "cancelled" {
		return nil
	}
	r.Result = parse.Truncate(r.Result, 256<<10)
	r.Error = parse.Truncate(r.Error, 8<<10)
	body, _ := json.Marshal(r)
	if err := h.S.FinishCommand(ctx, id, r.Status, string(body)); err != nil {
		return err
	}
	if r.Final() {
		h.logf("agent: command %d on %s %s%s", id, c.Host, r.Status, errSuffix(r.Error))
	}
	if c.Kind != KindTurn || c.AgentID == 0 {
		return nil
	}
	u := store.AgentUpdate{ProviderSession: r.SessionID, Cwd: r.Cwd}
	zero, empty := 0, ""
	switch r.Status {
	case "running":
		u.Status = "running"
		if r.PID > 0 {
			u.PID = &r.PID
		}
	case "done":
		u.Status, u.PID, u.Error = "idle", &zero, &empty
		u.LastResult = &r.Result
	case "failed":
		u.Status, u.PID, u.Error = "failed", &zero, &r.Error
		if r.Result != "" {
			u.LastResult = &r.Result
		}
	case "stopped":
		msg := "stopped"
		u.Status, u.PID, u.Error = "stopped", &zero, &msg
	}
	return h.S.UpdateAgentSession(ctx, c.AgentID, u)
}

func errSuffix(e string) string {
	if e == "" {
		return ""
	}
	return ": " + parse.Truncate(e, 300)
}

func (h *LocalHub) Inventory(ctx context.Context, inv Inventory) error {
	if inv.Host == "" {
		return fmt.Errorf("inventory needs host")
	}
	if len(inv.Folders) > 2000 {
		inv.Folders = inv.Folders[:2000]
	}
	var provs []string
	for _, p := range inv.Providers {
		if KnownProvider(p) {
			provs = append(provs, p)
		}
	}
	return h.S.SetAgentInventory(ctx, inv.Host, inv.Version, inv.Roots, inv.Folders, provs)
}
