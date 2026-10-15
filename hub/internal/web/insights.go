package web

import (
	"fmt"
	"html/template"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/charts"
	"github.com/anchoo2kewl/llm-brain/hub/internal/insights"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// registerInsights adds the /insights page and its JSON API.
func (s *Server) registerInsights(mux *http.ServeMux) {
	if s.Insights == nil {
		s.Insights = insights.NewCache(s.Store)
	}
	mux.HandleFunc("GET /insights", s.insightsPage)
	mux.HandleFunc("GET /api/insights", s.tailnetOnly(s.insightsAPI))
	mux.HandleFunc("GET /api/insights/{section}", s.tailnetOnly(s.insightsAPI))
	mux.HandleFunc("GET /api/insights/sessions/{id}", s.tailnetOnly(s.sessionStatsAPI))
}

func (s *Server) insightsPage(w http.ResponseWriter, r *http.Request) {
	rg := insights.ParseRange(r.URL.Query().Get("range"))
	start := time.Now()
	rep, err := s.Insights.Get(r.Context(), rg)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "insights", "layout", struct {
		page
		R      *insights.Report
		Ranges []insights.Range
		Took   time.Duration
	}{page{Title: "Insights", Nav: "insights"}, rep, insights.Ranges, time.Since(start).Round(time.Millisecond)})
}

// insightsAPI serves the report, or one section of it, as JSON: /api/insights/llm?range=30d.
func (s *Server) insightsAPI(w http.ResponseWriter, r *http.Request) {
	rep, err := s.Insights.Get(r.Context(), insights.ParseRange(r.URL.Query().Get("range")))
	if err != nil {
		s.fail(w, err)
		return
	}
	meta := map[string]any{"range": rep.Range, "from": rep.From, "to": rep.To, "labels": rep.Labels, "generated": rep.Generated}
	sections := map[string]any{"kpis": rep.KPIs, "callouts": rep.Callouts, "llm": rep.LLM, "quota": rep.Quota, "cost": rep.Cost,
		"sessions": rep.Sessions, "fleet": rep.Fleet, "chat": rep.Chat}
	sec := r.PathValue("section")
	if sec == "" {
		writeJSON(w, http.StatusOK, rep)
		return
	}
	v, ok := sections[sec]
	if !ok {
		names := make([]string, 0, len(sections))
		for k := range sections {
			names = append(names, k)
		}
		sort.Strings(names)
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown section", "sections": names})
		return
	}
	meta[sec] = v
	writeJSON(w, http.StatusOK, meta)
}

func (s *Server) sessionStatsAPI(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	st, err := insights.ForSession(r.Context(), s.Store, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// sessionStats is the header strip of a run or session page; nil when the session can't be read.
func (s *Server) sessionStats(r *http.Request, id int64) *insights.SessionStats {
	if id <= 0 {
		return nil
	}
	st, err := insights.ForSession(r.Context(), s.Store, id)
	if err != nil {
		return nil
	}
	return &st
}

// ---------------------------------------------------------------- template helpers

func unitFormat(unit string) func(float64) string {
	switch unit {
	case "dollars":
		return charts.Dollars
	case "percent":
		return charts.Percent
	case "ms":
		return charts.Millis
	case "minutes":
		return minutes
	}
	return charts.Compact
}

func minutes(m float64) string {
	switch {
	case m <= 0:
		return "0m"
	case m < 1:
		return fmt.Sprintf("%.0fs", m*60)
	case m < 90:
		return fmt.Sprintf("%.0fm", m)
	case m < 48*60:
		return fmt.Sprintf("%.1fh", m/60)
	}
	return fmt.Sprintf("%.1fd", m/1440)
}

// delta is a KPI's change against the previous period.
type delta struct {
	Text, Class string
}

func kpiDelta(k insights.KPI) delta {
	if !k.HasPrev {
		return delta{}
	}
	if k.Prev == 0 {
		if k.Value == 0 {
			return delta{"no change", "mut"}
		}
		return delta{"new", "mut"}
	}
	d := (k.Value - k.Prev) / k.Prev * 100
	up := d > 0
	text := fmt.Sprintf("%+.0f%%", d)
	if math.Abs(d) >= 1000 {
		text = fmt.Sprintf("%.0f×", k.Value/k.Prev)
	}
	if math.Abs(d) < 1 {
		return delta{"±0%", "mut"}
	}
	arrow := "▲ "
	if !up {
		arrow = "▼ "
	}
	cls := "ok"
	if up != k.UpGood {
		cls = "bad"
	}
	return delta{arrow + text, cls}
}

func toolBars(ts []insights.ToolStat, n int) []charts.Bar {
	out := make([]charts.Bar, 0, n)
	for _, t := range ts {
		if len(out) == n {
			break
		}
		b := charts.Bar{Label: t.Tool, Value: float64(t.Calls)}
		if t.Errors > 0 {
			b.Note = fmt.Sprintf("%.0f%% errors", float64(t.Errors)*100/float64(t.Calls))
			if t.Errors*5 >= t.Calls {
				b.Class = "bad"
			}
		}
		out = append(out, b)
	}
	return out
}

func latencyBars(ls []insights.ModelLatency) []charts.Bar {
	out := make([]charts.Bar, 0, len(ls))
	for _, l := range ls {
		note := fmt.Sprintf("p50 %s · %d calls", charts.Millis(l.P50), l.N)
		if l.PrevP95 > 0 {
			note += " · was " + charts.Millis(l.PrevP95)
		}
		out = append(out, charts.Bar{Label: l.Model, Value: l.P95, Note: note})
	}
	return out
}

func shareSlices(sh []insights.AccountShare) []charts.Slice {
	var personal, work, other float64
	for _, a := range sh {
		switch a.Class {
		case "personal":
			personal += float64(a.Tokens)
		case "work":
			work += float64(a.Tokens)
		default:
			other += float64(a.Tokens)
		}
	}
	out := []charts.Slice{{Label: "Personal", Value: personal, Class: "s0"}, {Label: "Work", Value: work, Class: "s1"}}
	if other > 0 {
		out = append(out, charts.Slice{Label: "Unknown", Value: other, Class: "s7"})
	}
	return out
}

func hourLabels() []string {
	out := make([]string, 24)
	for i := range out {
		out[i] = fmt.Sprintf("%02d", i)
	}
	return out
}

func heat(h [7][24]float64) template.HTML {
	cells := make([][]float64, 7)
	for d := range h {
		cells[d] = h[d][:]
	}
	return charts.Heatmap(charts.Opts{ID: "heat-chart", Title: "Prompts by weekday and hour",
		Desc: "Prompts you typed, by local weekday and hour of day; darker is busier."},
		[]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}, hourLabels(), cells)
}

func sessionBars(refs []insights.SessionRef, by string) []charts.Bar {
	out := make([]charts.Bar, 0, len(refs))
	for _, r := range refs {
		b := charts.Bar{Label: trunc(70, r.Title), Href: "/sessions/" + strconv.FormatInt(r.ID, 10)}
		switch by {
		case "dollars":
			b.Value = r.Dollars
			b.Note = charts.Compact(float64(r.Tokens)) + " tokens · " + r.Host
		default:
			b.Value = r.Hours
			b.Note = fmt.Sprintf("%d blocks · %s", r.Messages, r.Host)
		}
		out = append(out, b)
	}
	return out
}

func init() {
	for k, v := range map[string]any{
		"area": func(id, title string, labels []string, s []charts.Series, unit string, stacked bool) template.HTML {
			return charts.Area(charts.Opts{ID: id, Title: title, Desc: title + ", " + strconv.Itoa(len(labels)) + " intervals",
				XLabels: labels, Stacked: stacked, Format: unitFormat(unit)}, s)
		},
		"linechart": func(id, title string, labels []string, s []charts.Series, unit string, top float64) template.HTML {
			return charts.Area(charts.Opts{ID: id, Title: title, Desc: title, XLabels: labels, Lines: true, Max: top, Format: unitFormat(unit), Height: 140}, s)
		},
		"bars": func(id, title string, labels []string, s []charts.Series, unit string) template.HTML {
			return charts.Bars(charts.Opts{ID: id, Title: title, Desc: title, XLabels: labels, Format: unitFormat(unit)}, s)
		},
		"hbars": func(id, title string, b []charts.Bar, unit string) template.HTML {
			return charts.HBars(id, title, b, unitFormat(unit))
		},
		"legend": func(s []charts.Series, unit string) template.HTML { return charts.LegendOf(s, unitFormat(unit)) },
		"spark":  func(v []float64, title, cls string) template.HTML { return charts.Sparkline(v, title, cls) },
		"donut": func(id, title string, sl []charts.Slice, center, unit string) template.HTML {
			return charts.Donut(id, title, sl, center, unitFormat(unit))
		},
		"sliceLegend": func(sl []charts.Slice, unit string) template.HTML {
			f := unitFormat(unit)
			items := make([]charts.LegendItem, len(sl))
			for i, x := range sl {
				items[i] = charts.LegendItem{Name: x.Label, Class: x.Class, Value: f(x.Value)}
			}
			return charts.Legend(items)
		},
		"gauge": func(q insights.QuotaView) template.HTML {
			sub := q.Window
			if !q.ResetAt.IsZero() {
				sub += " · resets " + q.ResetAt.Local().Format("Mon 15:04")
			}
			return charts.Gauge("g-"+q.ID, fmt.Sprintf("%s %s %s window: %.0f%% used", q.Provider, q.Class, q.Window, q.UsedPct),
				q.UsedPct, fmt.Sprintf("%.0f%% left", math.Max(0, 100-q.UsedPct)), sub)
		},
		"heat":        heat,
		"fmt":         func(unit string, v float64) string { return unitFormat(unit)(v) },
		"fmti":        func(v int64) string { return charts.Compact(float64(v)) },
		"kpiDelta":    kpiDelta,
		"toolBars":    toolBars,
		"latencyBars": latencyBars,
		"shareSlices": shareSlices,
		"sessionBars": sessionBars,
		"hourLabels":  hourLabels,
		"workHours": func(h []float64) []charts.Series {
			return []charts.Series{{Name: "Work requests", Values: h, Class: "s1"}}
		},
		"pctOf": func(a, b int) string {
			if b == 0 {
				return "–"
			}
			return fmt.Sprintf("%.0f%%", float64(a)*100/float64(b))
		},
		"minutes":   minutes,
		"localTime": func(t time.Time) string { return t.Local().Format("Mon Jan 2 15:04") },
		"calloutIcon": func(level string) string {
			switch level {
			case "bad":
				return "!"
			case "warn":
				return "▲"
			case "good":
				return "✓"
			}
			return "i"
		},
		"rangeURL": func(base, key string) string { return base + "?range=" + key },
		"hasSeries": func(s []charts.Series) bool {
			for _, x := range s {
				for _, v := range x.Values {
					if v != 0 {
						return true
					}
				}
			}
			return false
		},
		"joinStr": strings.Join,
		// ownedWith counts issues carrying a label that contains part (working, pr-open, blocked).
		"ownedWith": func(is []Issue, part string) int {
			n := 0
			for _, i := range is {
				for _, l := range i.Labels {
					if strings.Contains(l.Name, part) {
						n++
						break
					}
				}
			}
			return n
		},
		"logSplit":   logSplit,
		"groupTools": groupTools,
		"span": func(a, b time.Time) string {
			if a.IsZero() || b.IsZero() {
				return "—"
			}
			return minutes(b.Sub(a).Minutes())
		},
		"lastEvent": func(es []store.Event) *store.Event {
			if len(es) == 0 {
				return nil
			}
			return &es[len(es)-1]
		},
		"hostSpark": func(m map[string][]float64, host string) []float64 { return m[host] },
		"topCallouts": func(cs []insights.Callout, n int) []insights.Callout {
			if len(cs) > n {
				return cs[:n]
			}
			return cs
		},
		"homeBars": func(b []charts.Bar) []charts.Bar {
			out := make([]charts.Bar, len(b))
			for i, x := range b {
				x.Label = homeShort(x.Label)
				out[i] = x
			}
			return out
		},
		"slice1": func(v []float64) []charts.Series { return []charts.Series{{Name: "Used", Values: v, Class: "s0"}} },
		"add":    func(a, b int) int { return a + b },
		"int64":  func(a int) int64 { return int64(a) },
	} {
		if _, taken := funcs[k]; taken {
			panic("insights: template func " + k + " already defined")
		}
		funcs[k] = v
	}
}

// logParts is a run log split for display: a long log shows its last lines, the rest behind an expander.
type logParts struct {
	Head, Tail           string
	HeadLines, TailLines int
}

const logTailLines = 60

func logSplit(s string) logParts {
	n := strings.Count(s, "\n")
	if n <= logTailLines*2 {
		return logParts{Tail: s}
	}
	cut := len(s)
	for i := 0; i <= logTailLines; i++ {
		j := strings.LastIndexByte(s[:cut], '\n')
		if j < 0 {
			return logParts{Tail: s}
		}
		cut = j
	}
	head, tail := s[:cut+1], s[cut+1:]
	return logParts{Head: head, Tail: tail, HeadLines: strings.Count(head, "\n"), TailLines: strings.Count(tail, "\n")}
}

// toolGroup is either one transcript item or a run of consecutive tool calls shown collapsed.
type toolGroup struct {
	One     item
	Run     []item
	Summary string
}

// minToolRun is the shortest run of consecutive tool calls folded into one expander.
const minToolRun = 3

func isTool(it item) bool { return it.Kind == "tool_use" || it.Kind == "tool_result" }

func groupTools(items []item) []toolGroup {
	var out []toolGroup
	for i := 0; i < len(items); {
		j := i
		for j < len(items) && isTool(items[j]) {
			j++
		}
		if j-i >= minToolRun {
			out = append(out, toolGroup{Run: items[i:j], Summary: toolRunSummary(items[i:j])})
			i = j
			continue
		}
		if j == i {
			j = i + 1
		}
		for ; i < j; i++ {
			out = append(out, toolGroup{One: items[i]})
		}
	}
	return out
}

func toolRunSummary(run []item) string {
	counts := map[string]int{}
	var order []string
	errs := 0
	for _, it := range run {
		name := it.ToolName
		if name == "" {
			name = "result"
		}
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
		if it.IsError || (it.Result != nil && it.Result.IsError) {
			errs++
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return counts[order[a]] > counts[order[b]] })
	parts := make([]string, 0, 4)
	for i, n := range order {
		if i == 4 {
			parts = append(parts, fmt.Sprintf("+%d more", len(order)-4))
			break
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", n, counts[n]))
	}
	s := fmt.Sprintf("%d tool calls · %s", len(run), strings.Join(parts, ", "))
	if errs > 0 {
		s += fmt.Sprintf(" · %d failed", errs)
	}
	return s
}
