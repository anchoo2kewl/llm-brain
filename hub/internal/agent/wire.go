// Package agent runs Claude and Codex sessions for the hub. The hub keeps a command queue; every machine
// runs an Executor that pulls its host's commands, runs each turn headless (claude -p / codex exec), and
// reports back. `hub serve` runs one against the store directly (LocalHub); `hub ship` runs one against the
// hub over HTTP (HTTPHub), so nothing listens on the laptop.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Command kinds.
const (
	KindTurn = "turn"
	KindStop = "stop"
)

// Permission modes (stored with the session; codex gets the matching sandbox).
const (
	PermPlan   = "plan"        // read-only
	PermEdit   = "acceptEdits" // edits in the folder (codex: workspace-write)
	PermBypass = "bypass"      // no approvals, no sandbox: needs explicit consent per session
)

// Turn is a turn command's payload: run one prompt in a session.
type Turn struct {
	Provider string `json:"provider"`
	Cwd      string `json:"cwd"`
	Model    string `json:"model,omitempty"`
	Perm     string `json:"perm"`
	Bypass   bool   `json:"bypass,omitempty"` // consent for PermBypass
	Prompt   string `json:"prompt"`
	Resume   string `json:"resume,omitempty"` // the CLI session id to continue
	Name     string `json:"name,omitempty"`   // claude --name on the first turn
	// Worktree, on a first turn: make <repo>-wt-<slug> from Base next to the repo and run there.
	Worktree *Worktree `json:"worktree,omitempty"`
	// Scratch: a chat with no project folder. The first turn makes <state>/chats/<session> on the host and
	// runs there; later turns run in that folder (Cwd), which is allowed even though it's outside the roots.
	Scratch bool `json:"scratch,omitempty"`
	// Review: ignore Prompt and review the folder's change against Base (read-only).
	Review *Review `json:"review,omitempty"`
}

type Worktree struct {
	Base string `json:"base"`
	Slug string `json:"slug"`
}

type Review struct {
	Base string `json:"base,omitempty"` // '' = the repo's default branch
}

// Stop is a stop command's payload.
type Stop struct {
	TurnCommand int64 `json:"turn_command,omitempty"`
}

// Report is what an executor says about a command. Status: running | done | failed | stopped.
type Report struct {
	Status    string `json:"status"`
	PID       int    `json:"pid,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	Result    string `json:"result,omitempty"` // the final answer
	Error     string `json:"error,omitempty"`
	Log       string `json:"log,omitempty"` // the turn's log file on that host
}

func (r Report) Final() bool { return r.Status != "running" }

// Inventory is what a host agent tells the hub about itself every minute.
type Inventory struct {
	Host      string              `json:"host"`
	Version   string              `json:"version"`
	Roots     []string            `json:"roots"`
	Folders   []store.AgentFolder `json:"folders"`
	Providers []string            `json:"providers"`
}

// Hub is the executor's side of the queue.
type Hub interface {
	// Poll returns the host's new commands. running lists the commands this executor still has in hand,
	// so the hub can fail the ones it lost (a restart).
	Poll(ctx context.Context, host, version string, running []int64) ([]store.Command, error)
	Report(ctx context.Context, id int64, r Report) error
	Inventory(ctx context.Context, inv Inventory) error
}

// ---------------------------------------------------------------- HTTP (hub ship)

type HTTPHub struct {
	Base   string
	Client *http.Client
}

func NewHTTPHub(base string) *HTTPHub {
	return &HTTPHub{Base: strings.TrimRight(base, "/"), Client: &http.Client{Timeout: 30 * time.Second}}
}

func (h *HTTPHub) Poll(ctx context.Context, host, version string, running []int64) ([]store.Command, error) {
	ids := make([]string, len(running))
	for i, id := range running {
		ids[i] = strconv.FormatInt(id, 10)
	}
	q := url.Values{"host": {host}, "version": {version}, "running": {strings.Join(ids, ",")}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.Base+"/api/agent/commands?"+q.Encode(), nil)
	var out []store.Command
	return out, h.do(req, &out)
}

func (h *HTTPHub) Report(ctx context.Context, id int64, r Report) error {
	return h.post(ctx, "/api/agent/commands/"+strconv.FormatInt(id, 10), r)
}

func (h *HTTPHub) Inventory(ctx context.Context, inv Inventory) error {
	return h.post(ctx, "/api/agent/inventory", inv)
}

func (h *HTTPHub) post(ctx context.Context, path string, v any) error {
	body, _ := json.Marshal(v)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, h.Base+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return h.do(req, nil)
}

func (h *HTTPHub) do(req *http.Request, out any) error {
	resp, err := h.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s %s: %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}
