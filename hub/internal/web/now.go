package web

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Now (/now): everything running, on every Mac, in one list — hub terminals, tasks, fleet workers,
// Claude/Codex sessions started outside the hub, and tool updates.

type nowCount struct {
	Terminals int `json:"terminals"`
	Tasks     int `json:"tasks"`
	Runs      int `json:"runs"`
	Sessions  int `json:"sessions"`
	Total     int `json:"total"`
}

// NowRow is one running thing.
type NowRow struct {
	Kind   string // terminal | task | run | session | update
	State  string // working | waiting | idle
	Title  string
	Detail string
	Host   string
	Where  string
	Link   string
	Since  string
	Ext    bool // started outside the hub
}

func (s *Server) nowRows(ctx context.Context) ([]NowRow, error) {
	var rows []NowRow
	var errs []error
	terms, err := s.Store.Terminals(ctx, "", 200)
	errs = append(errs, err)
	termSess := map[string]bool{}
	for _, t := range terms {
		if !t.Open() {
			continue
		}
		if t.SessionUUID != "" {
			termSess[t.Host+"|"+t.SessionUUID] = true
		}
		st := "working"
		if t.Status == "starting" {
			st = "idle"
		}
		rows = append(rows, NowRow{Kind: "terminal", State: st, Title: orUntitled(t.Title), Detail: t.Provider, Host: t.Host,
			Where: homeShort(t.Cwd), Link: "/terminals/" + itoa(t.ID), Since: firstNonEmpty(t.LiveAt, t.CreatedAt)})
	}
	tasks, err := s.Store.AgentSessions(ctx, 100)
	errs = append(errs, err)
	taskSess := map[string]bool{}
	for _, a := range tasks {
		if a.ProviderSession != "" {
			taskSess[a.Host+"|"+a.ProviderSession] = true
		}
		if !a.Busy() {
			continue
		}
		st := "working"
		if a.Status == "queued" {
			st = "idle"
		}
		rows = append(rows, NowRow{Kind: "task", State: st, Title: orUntitled(a.Title), Detail: a.Provider + " · " + a.Status,
			Host: a.Host, Where: homeShort(a.Cwd), Link: "/agents/" + itoa(a.ID), Since: a.UpdatedAt})
	}
	runs, err := s.Store.RunningRuns(ctx)
	errs = append(errs, err)
	linked := map[int64]bool{}
	for _, r := range runs {
		linked[r.SessionID] = true
		rows = append(rows, NowRow{Kind: "run", State: "working", Title: r.Key, Detail: r.Kind + " · " + r.Provider, Host: r.Host,
			Where: short(r.Target), Link: "/runs/" + itoa(r.ID), Since: r.StartedAt})
	}
	live, err := s.Store.LiveSessions(ctx)
	errs = append(errs, err)
	for _, x := range live {
		if linked[x.ID] || termSess[x.Host+"|"+x.UUID] || taskSess[x.Host+"|"+x.UUID] {
			continue
		}
		rows = append(rows, NowRow{Kind: "session", State: "working", Title: x.DisplayTitle(), Detail: x.Provider, Host: x.Host,
			Where: homeShort(x.Cwd), Link: "/sessions/" + itoa(x.ID), Since: x.FirstAt, Ext: true})
	}
	ups, err := s.Store.ToolUpdates(ctx, 10)
	errs = append(errs, err)
	for _, c := range ups {
		if !store.FinalStatus(c.Status) {
			rows = append(rows, NowRow{Kind: "update", State: "working", Title: "Updating tools", Host: c.Host,
				Link: "/settings/updates", Since: c.CreatedAt})
		}
	}
	return rows, errors.Join(errs...)
}

func (s *Server) nowCounts(ctx context.Context) (nowCount, error) {
	rows, err := s.nowRows(ctx)
	var c nowCount
	for _, r := range rows {
		switch r.Kind {
		case "terminal":
			c.Terminals++
		case "task":
			c.Tasks++
		case "run":
			c.Runs++
		case "session":
			c.Sessions++
		}
	}
	c.Total = len(rows)
	return c, err
}

func (s *Server) nowPage(w http.ResponseWriter, r *http.Request) {
	rows, err := s.nowRows(r.Context())
	if err != nil {
		s.Log.Printf("now: %v", err)
	}
	// Group by host, busiest host first; newest first inside.
	type group struct {
		Host string
		Rows []NowRow
	}
	idx := map[string]int{}
	var groups []*group
	for _, x := range rows {
		i, ok := idx[x.Host]
		if !ok {
			i = len(groups)
			idx[x.Host] = i
			groups = append(groups, &group{Host: x.Host})
		}
		groups[i].Rows = append(groups[i].Rows, x)
	}
	for _, g := range groups {
		sort.SliceStable(g.Rows, func(i, j int) bool { return parseAny(g.Rows[i].Since).After(parseAny(g.Rows[j].Since)) })
	}
	sort.SliceStable(groups, func(i, j int) bool { return len(groups[i].Rows) > len(groups[j].Rows) })
	hosts, _ := s.Store.AgentHosts(r.Context())
	tmpl := "layout"
	if r.URL.Query().Get("fragment") == "1" {
		tmpl = "content"
	}
	s.render(w, "now", tmpl, struct {
		page
		Groups []*group
		Total  int
		Hosts  []store.AgentHost
		At     time.Time
	}{page{Title: "Now", Nav: "now"}, groups, len(rows), hosts, time.Now()})
}
