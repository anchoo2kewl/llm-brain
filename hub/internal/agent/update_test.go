package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func noTools(context.Context) []store.ToolInfo { return nil }

func TestInstallMethod(t *testing.T) {
	home := "/Users/me"
	for _, c := range []struct{ name, real, want string }{
		{"brew", "/opt/homebrew/Homebrew/bin/brew", "brew"},
		{"claude", "/opt/homebrew/Caskroom/claude-code@latest/2.1.300/claude", "cask:claude-code@latest"},
		{"codex", "/opt/homebrew/Caskroom/codex/0.159.3/bin/codex", "cask:codex"},
		{"gemini", "/opt/homebrew/Cellar/gemini-cli/0.42.0/bin/gemini", "formula:gemini-cli"},
		{"gemini", "/usr/local/lib/node_modules/@google/gemini-cli/dist/index.js", "npm:@google/gemini-cli"},
		{"ocr", "/opt/homebrew/lib/node_modules/@alibaba-group/open-code-review/bin/ocr.js", "npm:@alibaba-group/open-code-review"},
		{"claude", "/Users/me/.local/share/claude/versions/2.1.3", "self"},
		{"codex", "/Users/me/.local/share/claude/versions/2.1.3", ""}, // only claude updates itself
		{"gh", "/usr/bin/gh", ""},
	} {
		if got := InstallMethod(c.name, c.real, home); got != c.want {
			t.Errorf("%s %s: got %q want %q", c.name, c.real, got, c.want)
		}
	}
}

func TestVersion(t *testing.T) {
	for in, want := range map[string]string{
		"2.1.300 (Claude Code)\n":    "2.1.300",
		"codex-cli 0.159.3":          "0.159.3",
		"Homebrew 4.6.1\nHomebrew/":  "4.6.1",
		"gh version 2.80.0 (2026-1)": "2.80.0",
		"0.42.0-nightly.1":           "0.42.0-nightly.1",
		"no version here":            "",
	} {
		if got := Version(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

// updHub records reports and inventories.
type updHub struct {
	mu   sync.Mutex
	reps []Report
	invs []Inventory
}

func (h *updHub) Poll(context.Context, string, string, []int64) ([]store.Command, error) {
	return nil, nil
}
func (h *updHub) Report(_ context.Context, _ int64, r Report) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reps = append(h.reps, r)
	return nil
}
func (h *updHub) Inventory(_ context.Context, inv Inventory) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.invs = append(h.invs, inv)
	return nil
}

func TestToolUpdateRunsEachWay(t *testing.T) {
	hub := &updHub{}
	var mu sync.Mutex
	var ran []string
	upgraded := false
	ex := &Executor{Host: "studio", Hub: hub, StateDir: t.TempDir(), Providers: []string{},
		FindTools: func(context.Context) []store.ToolInfo {
			mu.Lock()
			defer mu.Unlock()
			v := "2.1.0"
			if upgraded {
				v = "2.2.0"
			}
			return []store.ToolInfo{
				{Name: "brew", Path: "/opt/homebrew/bin/brew", Method: "brew", Version: "4.6.1"},
				{Name: "claude", Path: "/opt/homebrew/bin/claude", Method: "cask:claude-code@latest", Version: v},
				{Name: "gemini", Path: "/opt/homebrew/bin/gemini", Method: "formula:gemini-cli", Version: "0.42.0"},
				{Name: "gh", Path: "/usr/bin/gh", Version: "2.80.0"}, // installed some other way
			}
		},
		UpdateRun: func(_ context.Context, env []string, name string, args ...string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			ran = append(ran, filepath.Base(name)+" "+strings.Join(args, " "))
			if !hasEnv(env, "HOMEBREW_NO_AUTO_UPDATE=1") {
				t.Errorf("brew would update itself on every call: %v", env)
			}
			if len(args) > 1 && args[1] == "--formula" {
				return "Error: gemini-cli is pinned", errors.New("exit status 1")
			}
			if len(args) > 1 && args[1] == "--cask" {
				upgraded = true
			}
			return "ok\n", nil
		}}
	ex.init()
	payload, _ := json.Marshal(ToolUpdate{Tools: []string{"claude", "gemini", "gh", "codex"}})
	ex.toolUpdate(context.Background(), store.Command{ID: 7, Kind: KindToolUpdate, Payload: payload})

	want := []string{"brew update --quiet", "brew upgrade --cask claude-code@latest", "brew upgrade --formula gemini-cli"}
	if strings.Join(ran, "|") != strings.Join(want, "|") {
		t.Fatalf("ran %q, want %q", ran, want)
	}
	if len(hub.reps) != 2 || hub.reps[0].Status != "running" {
		t.Fatalf("reports %+v", hub.reps)
	}
	final := hub.reps[1]
	if final.Status != "failed" || !strings.Contains(final.Error, "gemini") {
		t.Fatalf("final %+v", final)
	}
	var res UpdateResult
	if err := json.Unmarshal([]byte(final.Result), &res); err != nil {
		t.Fatal(err)
	}
	by := map[string]ToolResult{}
	for _, r := range res.Tools {
		by[r.Name] = r
	}
	if c := by["claude"]; !c.OK || c.Before != "2.1.0" || c.After != "2.2.0" || !c.Changed() {
		t.Errorf("claude %+v", c)
	}
	if g := by["gemini"]; g.OK || !strings.Contains(g.Output, "pinned") {
		t.Errorf("gemini %+v", g)
	}
	if by["gh"].Skip == "" || by["codex"].Skip != "not installed" {
		t.Errorf("skips: gh %+v codex %+v", by["gh"], by["codex"])
	}
	if len(hub.invs) != 1 || len(hub.invs[0].Tools) != 4 || hub.invs[0].Tools[1].Version != "2.2.0" {
		t.Errorf("the hub should hear the new versions right away: %+v", hub.invs)
	}
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func TestToolUpdateQueueAndReport(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := QueueToolUpdate(ctx, r.st, "studio", []string{"nope"}, false); err == nil {
		t.Fatal("unknown tools only: want an error")
	}
	if _, err := QueueToolUpdate(ctx, r.st, "laptop", []string{"claude"}, false); err == nil {
		t.Fatal("unknown host: want an error")
	}
	id, err := QueueToolUpdate(ctx, r.st, "studio", []string{"claude", "nope"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := QueueToolUpdate(ctx, r.st, "studio", []string{"claude"}, false); err == nil || !strings.Contains(err.Error(), "already updating") {
		t.Fatalf("second update while one is queued: %v", err)
	}
	r.ex.FindTools = func(context.Context) []store.ToolInfo {
		return []store.ToolInfo{{Name: "claude", Path: "/x/claude", Method: "self", Version: "1.0.0"}}
	}
	r.ex.UpdateRun = func(_ context.Context, _ []string, name string, args ...string) (string, error) {
		if name != "/x/claude" || strings.Join(args, " ") != "update" {
			t.Errorf("ran %s %v", name, args)
		}
		return "Updated", nil
	}
	r.cycle(t)
	c, err := r.st.Command(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "done" {
		t.Fatalf("status %s: %s", c.Status, c.Result)
	}
	res, e := ParseUpdateResult(c.Result)
	if e != "" || len(res.Tools) != 1 || !res.Tools[0].OK {
		t.Fatalf("result %+v %q", res, e)
	}
	tools, _ := r.st.AgentTools(ctx)
	if len(tools["studio"]) != 1 || tools["studio"][0].Method != "self" {
		t.Fatalf("tools %+v", tools)
	}
	// A repeated report after the end is ignored.
	if err := r.hub.Report(ctx, id, Report{Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := r.st.Command(ctx, id); c.Status != "done" {
		t.Fatalf("late report moved it to %s", c.Status)
	}
}

func TestUpdateSettingsAndDailyTick(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	set, err := LoadUpdateSettings(ctx, r.st)
	if err != nil || set.Auto || set.Hour != 4 || len(set.Tools) != len(Tools) {
		t.Fatalf("defaults %+v %v", set, err)
	}
	now := time.Date(2026, 10, 15, 3, 0, 0, 0, time.Local)
	u := &Updater{S: r.st, Now: func() time.Time { return now }}
	if err := u.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if busy, _ := r.st.ToolUpdateActive(ctx, "studio"); busy {
		t.Fatal("off by default, yet it queued")
	}
	if err := SaveUpdateSettings(ctx, r.st, UpdateSettings{Auto: true, Hour: 5, Tools: []string{"claude", "codex"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveUpdateSettings(ctx, r.st, UpdateSettings{Hour: 25}); err == nil {
		t.Fatal("hour 25 accepted")
	}
	_ = u.Tick(ctx) // 03:00, before 05:00
	if busy, _ := r.st.ToolUpdateActive(ctx, "studio"); busy {
		t.Fatal("queued before the hour")
	}
	now = now.Add(3 * time.Hour) // 06:00
	if err := u.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	cs, _ := r.st.ToolUpdates(ctx, 10)
	if len(cs) != 1 {
		t.Fatalf("want one daily update, got %d", len(cs))
	}
	var p ToolUpdate
	_ = json.Unmarshal(cs[0].Payload, &p)
	if !p.Auto || strings.Join(p.Tools, ",") != "claude,codex" {
		t.Fatalf("payload %+v", p)
	}
	// Once a day: finishing it and ticking again the same day queues nothing.
	_, _ = r.st.FinishToolUpdate(ctx, cs[0].ID, "done", "{}")
	_ = r.hub.Inventory(ctx, r.ex.inventory(ctx)) // keep the host live
	_ = u.Tick(ctx)
	if cs, _ := r.st.ToolUpdates(ctx, 10); len(cs) != 1 {
		t.Fatalf("second tick the same day queued again: %d", len(cs))
	}
	// Next day, but this host is skipped.
	now = now.Add(24 * time.Hour)
	if err := SaveUpdateSettings(ctx, r.st, UpdateSettings{Auto: true, Hour: 5, Tools: []string{"claude"}, Skip: []string{"studio"}}); err != nil {
		t.Fatal(err)
	}
	_ = u.Tick(ctx)
	if cs, _ := r.st.ToolUpdates(ctx, 10); len(cs) != 1 {
		t.Fatalf("a skipped host was updated")
	}
	// No tools ticked: stored as "-", loads as none, and nothing runs.
	if err := SaveUpdateSettings(ctx, r.st, UpdateSettings{Auto: true, Hour: 5}); err != nil {
		t.Fatal(err)
	}
	if set, _ := LoadUpdateSettings(ctx, r.st); len(set.Tools) != 0 || !set.Auto {
		t.Fatalf("none ticked: %+v", set)
	}
}
