package web

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Tool updates (/settings/updates): keep brew, Claude Code, Codex, Gemini CLI and friends current on every
// host, daily or by hand. The agent on each host does the work (agent/update.go).

func (s *Server) updateRoutes(g, p func(string, http.HandlerFunc)) {
	g("GET /settings/updates", s.updatesPage)
	p("POST /settings/updates", s.updatesSave)
	p("POST /settings/updates/run", s.updatesRun)
}

type updHost struct {
	Host   string
	Live   bool
	Skip   bool
	Busy   bool
	Tools  []store.ToolInfo
	Last   string // local date of the last daily update
	Policy string // old agent: no tool list yet
}

type updRun struct {
	store.Command
	Auto    bool
	Tools   []string
	Results []agent.ToolResult
	Error   string
}

func (s *Server) updatesPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	set, err := agent.LoadUpdateSettings(ctx, s.Store)
	if err != nil {
		s.fail(w, err)
		return
	}
	hosts, err := s.Store.AgentHosts(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	tools, err := s.Store.AgentTools(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	cmds, err := s.Store.ToolUpdates(ctx, 30)
	if err != nil {
		s.fail(w, err)
		return
	}
	var hs []updHost
	for _, h := range hosts {
		u := updHost{Host: h.Host, Live: h.Live(), Skip: set.Skips(h.Host), Tools: tools[h.Host]}
		u.Busy, _ = s.Store.ToolUpdateActive(ctx, h.Host)
		u.Last, _ = s.Store.UpdateLast(ctx, h.Host)
		hs = append(hs, u)
	}
	sort.SliceStable(hs, func(i, j int) bool { return hs[i].Live && !hs[j].Live })
	var runs []updRun
	for _, c := range cmds {
		x := updRun{Command: c}
		var p agent.ToolUpdate
		_ = json.Unmarshal(c.Payload, &p)
		x.Auto, x.Tools = p.Auto, p.Tools
		res, e := agent.ParseUpdateResult(c.Result)
		x.Results, x.Error = res.Tools, e
		runs = append(runs, x)
	}
	type toolOpt struct {
		Name, Label string
		On          bool
	}
	var opts []toolOpt
	for _, t := range agent.Tools {
		opts = append(opts, toolOpt{t.Name, t.Label, set.Wants(t.Name)})
	}
	hours := make([]int, 24)
	for i := range hours {
		hours[i] = i
	}
	s.render(w, "updates", "layout", struct {
		page
		Set     agent.UpdateSettings
		Opts    []toolOpt
		Hours   []int
		Hosts   []updHost
		Runs    []updRun
		Saved   bool
		Queued  string
		Err     string
		TZ      string
		Pending bool
	}{page: page{Title: "Updates", Nav: "settings"}, Set: set, Opts: opts, Hours: hours, Hosts: hs, Runs: runs,
		Saved: r.URL.Query().Get("saved") == "1", Queued: r.URL.Query().Get("queued"), Err: r.URL.Query().Get("err"),
		TZ: time.Now().Format("MST"), Pending: anyActive(runs)})
}

func anyActive(rs []updRun) bool {
	for _, r := range rs {
		if !store.FinalStatus(r.Status) {
			return true
		}
	}
	return false
}

func (s *Server) updatesSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	set := agent.UpdateSettings{Auto: r.PostFormValue("auto") == "1", Tools: r.PostForm["tool"]}
	set.Hour, _ = strconv.Atoi(r.PostFormValue("hour"))
	// The form lists every host with an "update daily" box; the unticked ones are skipped.
	daily := map[string]bool{}
	for _, h := range r.PostForm["daily"] {
		daily[h] = true
	}
	for _, h := range r.PostForm["host"] {
		if !daily[h] {
			set.Skip = append(set.Skip, h)
		}
	}
	if err := agent.SaveUpdateSettings(r.Context(), s.Store, set); err != nil {
		s.back(w, r, "/settings/updates", err)
		return
	}
	http.Redirect(w, r, "/settings/updates?saved=1", http.StatusSeeOther)
}

// updatesRun: "Update now", for one host or every live one, with the ticked tools or one tool.
func (s *Server) updatesRun(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	tools := r.PostForm["tool"]
	if len(tools) == 0 {
		set, err := agent.LoadUpdateSettings(ctx, s.Store)
		if err != nil {
			s.fail(w, err)
			return
		}
		tools = set.Tools
	}
	var hosts []string
	if h := r.PostFormValue("host"); h != "" {
		hosts = []string{h}
	} else {
		all, err := s.Store.AgentHosts(ctx)
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, h := range all {
			if h.Live() {
				hosts = append(hosts, h.Host)
			}
		}
	}
	var queued []string
	var firstErr error
	for _, h := range hosts {
		if _, err := agent.QueueToolUpdate(ctx, s.Store, h, tools, false); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		queued = append(queued, h)
	}
	if len(queued) == 0 {
		if firstErr == nil {
			firstErr = agent.InputError{Msg: "no host agent is running"}
		}
		s.back(w, r, "/settings/updates", firstErr)
		return
	}
	http.Redirect(w, r, "/settings/updates?queued="+strings.Join(queued, ","), http.StatusSeeOther)
}
