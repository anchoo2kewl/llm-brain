package insights

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/charts"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Report is everything /insights shows for one range. Series are aligned with Labels (one value per bucket).
type Report struct {
	Range     Range     `json:"range"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Labels    []string  `json:"labels"`
	Axis      Axis      `json:"axis"`
	Generated time.Time `json:"generated"`
	TookMS    int64     `json:"took_ms"`
	// RollupPct < 100 while the background rollup is still reading history: session-derived numbers are partial.
	RollupPct int `json:"rollup_pct"`

	KPIs     []KPI        `json:"kpis"`
	Callouts []Callout    `json:"callouts"`
	LLM      LLM          `json:"llm"`
	Quota    []QuotaView  `json:"quota"`
	Cost     CostView     `json:"cost"`
	Sessions SessionsView `json:"sessions"`
	Fleet    FleetView    `json:"fleet"`
	Chat     ChatView     `json:"chat"`

	buckets Buckets
}

// KPI is a headline number with its trend and the previous period's value.
type KPI struct {
	ID      string    `json:"id"`
	Label   string    `json:"label"`
	Value   float64   `json:"value"`
	Prev    float64   `json:"prev"`
	HasPrev bool      `json:"has_prev"`
	Unit    string    `json:"unit"` // count | dollars | percent
	UpGood  bool      `json:"up_good"`
	Spark   []float64 `json:"spark"`
	Href    string    `json:"href"`
}

// LLM is model usage: session transcripts (long history, Claude usage per response) and the gateway (every
// routed request, with account and latency).
type LLM struct {
	TokensByModel    []charts.Series `json:"tokens_by_model"`
	TokenKinds       []charts.Series `json:"token_kinds"` // uncached input, cache write, cache read, output
	Totals           Tokens          `json:"totals"`
	PrevTotal        int64           `json:"prev_total"`
	CacheShare       float64         `json:"cache_share"` // % of input served from cache
	GwRequestsByAcct []charts.Series `json:"gw_requests_by_account"`
	GwTokensByModel  []charts.Series `json:"gw_tokens_by_model"`
	GwRequests       int             `json:"gw_requests"`
	GwFailed         int             `json:"gw_failed"`
	GwRateLimited    int             `json:"gw_rate_limited"`
	GwErrorsByCode   map[int]int     `json:"gw_errors_by_code"`
	GwErrorSeries    []charts.Series `json:"gw_error_series"`
	Latency          []ModelLatency  `json:"latency"`
	Share            []AccountShare  `json:"share"`
	WorkTokenPct     float64         `json:"work_token_pct"`
	WorkHours        []float64       `json:"work_hours"`        // requests served by work accounts, by local hour of day
	WorkWhileCapped  int             `json:"work_while_capped"` // work requests made while a personal window was >= 95%
	WorkRequests     int             `json:"work_requests"`
}

type ModelLatency struct {
	Model   string  `json:"model"`
	N       int     `json:"n"`
	P50     float64 `json:"p50_ms"`
	P95     float64 `json:"p95_ms"`
	PrevP95 float64 `json:"prev_p95_ms"` // 0: no requests in the previous period
	PrevN   int     `json:"prev_n"`
}

type AccountShare struct {
	Account  string `json:"account"`
	Provider string `json:"provider"`
	Class    string `json:"class"` // personal | work | unknown
	Requests int    `json:"requests"`
	Tokens   int64  `json:"tokens"`
}

// QuotaView is one quota window of one account, with its burn rate and when it runs out at that rate.
type QuotaView struct {
	ID          string    `json:"id"`
	Account     string    `json:"account"`
	Provider    string    `json:"provider"`
	Class       string    `json:"class"`
	Window      string    `json:"window"`
	UsedPct     float64   `json:"used_pct"`
	ResetAt     time.Time `json:"reset_at"`
	Observed    time.Time `json:"observed"`
	BurnPerHour float64   `json:"burn_pct_per_hour"`
	BurnFrom    string    `json:"burn_from"`   // history | average | ""
	RunsOutAt   time.Time `json:"runs_out_at"` // zero: not at this rate before reset
	Verdict     string    `json:"verdict"`
	History     []float64 `json:"history"` // used % per bucket (carried forward)
	HasHistory  bool      `json:"has_history"`
	Serving     bool      `json:"serving"`
	Exhausted   bool      `json:"exhausted"`
}

type CostView struct {
	Models    []ModelCost     `json:"models"`
	Total     float64         `json:"total"`
	Prev      float64         `json:"prev"`
	Series    []charts.Series `json:"series"`  // dollars per bucket by model
	Gateway   float64         `json:"gateway"` // estimate over gateway-routed requests (includes Codex)
	Unpriced  []string        `json:"unpriced"`
	PriceRows []PriceRow      `json:"prices"`
}

type ModelCost struct {
	Model   string  `json:"model"`
	Tokens  Tokens  `json:"tokens"`
	Dollars float64 `json:"dollars"`
	Priced  bool    `json:"priced"`
}

type SessionRef struct {
	ID       int64   `json:"id"`
	Title    string  `json:"title"`
	Host     string  `json:"host"`
	Provider string  `json:"provider"`
	Cwd      string  `json:"cwd"`
	Hours    float64 `json:"active_hours"`
	Span     float64 `json:"span_hours"`
	Tokens   int64   `json:"tokens"`
	Dollars  float64 `json:"dollars"`
	Messages int64   `json:"messages"`
}

type ToolStat struct {
	Tool   string `json:"tool"`
	Calls  int    `json:"calls"`
	Errors int    `json:"errors"`
}

type SessionsView struct {
	StartedByHost     []charts.Series      `json:"started_by_host"`
	BlocksByHost      []charts.Series      `json:"blocks_by_host"`
	PromptsByProvider []charts.Series      `json:"prompts_by_provider"`
	Heat              [7][24]float64       `json:"heat"` // prompts by local weekday (Mon=0) and hour
	HeatPeak          string               `json:"heat_peak"`
	Projects          []charts.Bar         `json:"projects"`
	Started           int                  `json:"started"`
	PrevStarted       int                  `json:"prev_started"`
	Prompts           int64                `json:"prompts"`
	PrevPrompts       int64                `json:"prev_prompts"`
	Blocks            int64                `json:"blocks"`
	MedianActiveH     float64              `json:"median_active_hours"`
	AvgActiveH        float64              `json:"avg_active_hours"`
	MedianSpanMin     float64              `json:"median_span_minutes"`
	Tools             []ToolStat           `json:"tools"`
	ToolCalls         int                  `json:"tool_calls"`
	ToolErrors        int                  `json:"tool_errors"`
	PrevToolCalls     int                  `json:"prev_tool_calls"`
	Longest           []SessionRef         `json:"longest"`
	Costliest         []SessionRef         `json:"costliest"`
	HostSpark         map[string][]float64 `json:"host_spark"`
}

type FleetView struct {
	ByKind         []charts.Series `json:"by_kind"`
	ByOutcome      []charts.Series `json:"by_outcome"`
	Verdicts       []charts.Series `json:"verdicts"`
	Approve        int             `json:"approve"`
	Comment        int             `json:"comment"`
	OtherVerdict   int             `json:"other_verdict"`
	Red            int             `json:"red"`
	Optional       int             `json:"optional"`
	Blockers       []charts.Bar    `json:"blockers"`
	Kinds          []KindStat      `json:"kinds"`
	Repos          []charts.Bar    `json:"repos"`
	Cycles         []Cycle         `json:"cycles"`
	MedianCycleMin float64         `json:"median_cycle_minutes"`
	Runs           int             `json:"runs"`
	PrevRuns       int             `json:"prev_runs"`
	Failed         int             `json:"failed"`
}

type KindStat struct {
	Kind      string  `json:"kind"`
	Runs      int     `json:"runs"`
	Succeeded int     `json:"succeeded"`
	Exited    int     `json:"exited"`
	Failed    int     `json:"failed"`
	Running   int     `json:"running"`
	MedianMin float64 `json:"median_minutes"`
}

// Cycle is one target (issue or PR) the fleet worked on: its runs and the time from first start to last end.
type Cycle struct {
	Target  string  `json:"target"`
	Key     string  `json:"key"`
	Kind    string  `json:"kind"`
	Runs    int     `json:"runs"`
	SpanMin float64 `json:"span_minutes"`
	BusyMin float64 `json:"busy_minutes"`
	LastRun int64   `json:"last_run_id"`

	first time.Time
}

type ChatView struct {
	ByConv   []charts.Series `json:"by_conversation"`
	People   []charts.Bar    `json:"people"`
	ToMe     []charts.Series `json:"to_me"` // mentions and DMs from others
	Threads  []Thread        `json:"threads"`
	Total    int             `json:"total"`
	Mentions int             `json:"mentions"`
	DMs      int             `json:"dms"`
	Enabled  bool            `json:"enabled"`
}

type Thread struct {
	Conv    string `json:"conversation"`
	ConvID  string `json:"conv_id"`
	TS      string `json:"thread_ts"`
	Replies int    `json:"messages"`
	People  int    `json:"people"`
	Last    string `json:"last_at"`
}

// ---------------------------------------------------------------- build

// earliestQueries find the first timestamp of each source "all time" covers. Each is an ordered LIMIT 1 on an
// indexed column (Turso v0.7 scans the whole table for MIN()).
var earliestQueries = []string{
	`SELECT first_at FROM sessions WHERE first_at != '' ORDER BY first_at LIMIT 1`,
	`SELECT ts FROM gateway_requests WHERE ts != '' ORDER BY ts LIMIT 1`,
	`SELECT started_at FROM runs WHERE started_at != '' ORDER BY started_at LIMIT 1`,
	`SELECT at FROM chat_messages WHERE at != '' ORDER BY at LIMIT 1`,
	`SELECT ts FROM usage_events WHERE ts != '' ORDER BY ts LIMIT 1`,
}

// Earliest is the first timestamp across every source the report covers (zero when the hub is empty).
func Earliest(ctx context.Context, db *sql.DB) time.Time {
	var first time.Time
	for _, q := range earliestQueries {
		var v string
		if db.QueryRowContext(ctx, q).Scan(&v) != nil {
			continue
		}
		if t := store.ParseT(v); !t.IsZero() && (first.IsZero() || t.Before(first)) {
			first = t
		}
	}
	return first
}

// Debug, when set, receives each build step's duration.
var Debug func(step string, d time.Duration)

// Build computes the report for r. now and loc are parameters so tests are deterministic.
func Build(ctx context.Context, st *store.Store, r Range, now time.Time, loc *time.Location) (*Report, error) {
	start := time.Now()
	db := st.DB
	var earliest time.Time
	if r.Dur == 0 {
		earliest = Earliest(ctx, db)
	}
	b := NewBuckets(r, now, earliest, loc)
	ax := b.Axis()
	rep := &Report{Range: r, From: b.From, To: b.To, Labels: ax.Labels, Axis: ax, Generated: now, buckets: b}
	rep.RollupPct = Progress(ctx, st)
	t0 := time.Now()
	sess, err := loadSessions(ctx, db)
	if Debug != nil {
		Debug("load", time.Since(t0))
	}
	if err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	accts, err := st.GatewayAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("gateway accounts: %w", err)
	}
	classes := accountClasses(accts)
	steps := []struct {
		name string
		fn   func() error
	}{
		{"usage", func() error { return rep.buildUsage(ctx, db, sess) }},
		{"gateway", func() error { return rep.buildGateway(ctx, db, accts, classes) }},
		{"quota", func() error { return rep.buildQuota(ctx, st, accts, classes, now) }},
		{"sessions", func() error { return rep.buildSessions(ctx, db, sess) }},
		{"fleet", func() error { return rep.buildFleet(ctx, db) }},
		{"chat", func() error { return rep.buildChat(ctx, db) }},
	}
	for _, s := range steps {
		t0 := time.Now()
		err := s.fn()
		if Debug != nil {
			Debug(s.name, time.Since(t0))
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.name, err)
		}
	}
	rep.buildKPIs()
	rep.Callouts = Callouts(rep)
	rep.TookMS = time.Since(start).Milliseconds()
	return rep, nil
}

func bound(t time.Time) string { return store.T(t) }

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func zeros(n int) []float64 { return make([]float64, n) }

// seriesSet accumulates named series over buckets.
type seriesSet struct {
	n     int
	order []string
	m     map[string][]float64
}

func newSet(n int) *seriesSet { return &seriesSet{n: n, m: map[string][]float64{}} }

func (s *seriesSet) add(name string, i int, v float64) {
	if i < 0 || i >= s.n {
		return
	}
	vals, ok := s.m[name]
	if !ok {
		vals = zeros(s.n)
		s.m[name] = vals
		s.order = append(s.order, name)
	}
	vals[i] += v
}

// series returns the set sorted by name (stable colours per entity), folded to the palette.
func (s *seriesSet) series() []charts.Series {
	names := append([]string(nil), s.order...)
	sort.Strings(names)
	out := make([]charts.Series, 0, len(names))
	for _, n := range names {
		out = append(out, charts.Series{Name: n, Values: s.m[n]})
	}
	return charts.Fold(out, charts.Palette)
}

// fixed returns series in a given order with given classes, including empty ones.
func (s *seriesSet) fixed(names, classes []string) []charts.Series {
	out := make([]charts.Series, len(names))
	for i, n := range names {
		v := s.m[n]
		if v == nil {
			v = zeros(s.n)
		}
		out[i] = charts.Series{Name: n, Values: v}
		if i < len(classes) {
			out[i].Class = classes[i]
		}
	}
	return out
}

func sum(v []float64) float64 {
	t := 0.0
	for _, x := range v {
		t += x
	}
	return t
}

func totals(series []charts.Series, n int) []float64 {
	out := zeros(n)
	for _, s := range series {
		for i, v := range s.Values {
			if i < n {
				out[i] += v
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- sessions table

type sessMeta struct {
	ID                                   int64
	Host, Provider, Cwd, Project, Parent string
	UUID, Title, Auto, Agent             string
	First, Last                          time.Time
	Messages                             int64
}

func (s sessMeta) title() string {
	x := store.Session{Title: s.Title, AutoTitle: s.Auto, AgentName: s.Agent}
	return x.DisplayTitle()
}

func (s sessMeta) where() string {
	if s.Cwd != "" {
		return s.Cwd
	}
	return s.Project
}

type sessIndex struct {
	byID map[int64]*sessMeta
	// root maps a subagent transcript to its top-level session id.
	root map[int64]int64
}

func loadSessions(ctx context.Context, db *sql.DB) (*sessIndex, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, host, provider, cwd, project, parent_uuid, session_uuid, title, auto_title,
		agent_name, first_at, last_at, message_count FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ix := &sessIndex{byID: map[int64]*sessMeta{}, root: map[int64]int64{}}
	byUUID := map[string]int64{}
	for rows.Next() {
		var s sessMeta
		var f, l string
		if err := rows.Scan(&s.ID, &s.Host, &s.Provider, &s.Cwd, &s.Project, &s.Parent, &s.UUID, &s.Title, &s.Auto,
			&s.Agent, &f, &l, &s.Messages); err != nil {
			return nil, err
		}
		s.First, s.Last = store.ParseT(f), store.ParseT(l)
		ix.byID[s.ID] = &s
		if s.Parent == "" && s.UUID != "" {
			byUUID[s.Host+"\x00"+s.UUID] = s.ID
		}
	}
	for id, s := range ix.byID {
		ix.root[id] = id
		if s.Parent != "" {
			if p, ok := byUUID[s.Host+"\x00"+s.Parent]; ok {
				ix.root[id] = p
			}
		}
	}
	return ix, rows.Err()
}

// ---------------------------------------------------------------- usage (transcripts)

// usageAgg is token usage of one session, one model, in one UTC hour.
type usageAgg struct {
	hour    time.Time
	session int64
	model   string
	t       Tokens
}

// queryUsage sums usage in [from, to) per session, model and hour (per minute when fine is set, for short
// ranges whose buckets don't sit on hour boundaries).
func queryUsage(ctx context.Context, db *sql.DB, from, to time.Time, fine bool) ([]usageAgg, error) {
	n := 13
	if fine {
		n = 16
	}
	rows, err := db.QueryContext(ctx, `SELECT substr(ts, 1, ?), session_id, model, SUM(input), SUM(output), SUM(cache_read), SUM(cache_write)
		FROM usage_events WHERE ts >= ? AND ts < ? GROUP BY 1, 2, 3`, n, bound(from), bound(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []usageAgg
	for rows.Next() {
		var h string
		var u usageAgg
		if err := rows.Scan(&h, &u.session, &u.model, &u.t.Input, &u.t.Output, &u.t.CacheRead, &u.t.CacheWrite); err != nil {
			return nil, err
		}
		if len(h) == 16 {
			u.hour, _ = time.Parse("2006-01-02T15:04", h)
		} else {
			u.hour = parseHour(h)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *Report) buildUsage(ctx context.Context, db *sql.DB, sess *sessIndex) error {
	b := r.buckets
	rows, err := queryUsage(ctx, db, b.From, b.To, b.Step > 0 && b.Step < 24*time.Hour)
	if err != nil {
		return err
	}
	byModel := newSet(b.N)
	kinds := newSet(b.N)
	cost := newSet(b.N)
	models := map[string]*ModelCost{}
	per := map[int64]*SessionRef{}
	for _, u := range rows {
		// Rows are already limited to the window; an hour that starts before it draws in the first bucket.
		i := b.Index(maxTime(u.hour, b.From))
		byModel.add(u.model, i, float64(u.t.Total()))
		kinds.add("Uncached input", i, float64(u.t.Input))
		kinds.add("Cache write", i, float64(u.t.CacheWrite))
		kinds.add("Cache read", i, float64(u.t.CacheRead))
		kinds.add("Output", i, float64(u.t.Output))
		c := Cost(u.model, u.t)
		cost.add(u.model, i, c)
		m := models[u.model]
		if m == nil {
			_, ok := PriceFor(u.model)
			m = &ModelCost{Model: u.model, Priced: ok}
			models[u.model] = m
		}
		m.Tokens.Add(u.t)
		m.Dollars += c
		r.LLM.Totals.Add(u.t)
		// Costliest sessions: a subagent's usage counts toward the session that started it.
		root := sess.root[u.session]
		if root == 0 {
			root = u.session
		}
		ref := per[root]
		if ref == nil {
			ref = sessionRef(sess, root)
			per[root] = ref
		}
		ref.Tokens += u.t.Total()
		ref.Dollars += c
	}
	r.LLM.TokensByModel = byModel.series()
	r.LLM.TokenKinds = kinds.fixed([]string{"Uncached input", "Cache write", "Cache read", "Output"}, []string{"s0", "s1", "s2", "s3"})
	in := r.LLM.Totals.Input + r.LLM.Totals.CacheRead + r.LLM.Totals.CacheWrite
	if in > 0 {
		r.LLM.CacheShare = float64(r.LLM.Totals.CacheRead) * 100 / float64(in)
	}
	r.Cost.Series = cost.series()
	for _, m := range models {
		r.Cost.Models = append(r.Cost.Models, *m)
		r.Cost.Total += m.Dollars
		if !m.Priced {
			r.Cost.Unpriced = append(r.Cost.Unpriced, m.Model)
		}
	}
	sort.Slice(r.Cost.Models, func(i, j int) bool { return r.Cost.Models[i].Dollars > r.Cost.Models[j].Dollars })
	sort.Strings(r.Cost.Unpriced)
	r.Cost.PriceRows = PriceTable()
	list := make([]SessionRef, 0, len(per))
	for _, v := range per {
		list = append(list, *v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Dollars > list[j].Dollars })
	r.Sessions.Costliest = list[:min(len(list), 8)]
	if r.Range.Dur == 0 {
		return nil
	}
	p := b.Prev()
	prev, err := db.QueryContext(ctx, `SELECT model, SUM(input), SUM(output), SUM(cache_read), SUM(cache_write)
		FROM usage_events WHERE ts >= ? AND ts < ? GROUP BY 1`, bound(p.From), bound(p.To))
	if err != nil {
		return err
	}
	defer prev.Close()
	for prev.Next() {
		var model string
		var t Tokens
		if err := prev.Scan(&model, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite); err != nil {
			return err
		}
		r.LLM.PrevTotal += t.Total()
		r.Cost.Prev += Cost(model, t)
	}
	return prev.Err()
}

func sessionRef(sess *sessIndex, id int64) *SessionRef {
	ref := &SessionRef{ID: id}
	if s := sess.byID[id]; s != nil {
		ref.Title, ref.Host, ref.Provider, ref.Cwd, ref.Messages = s.title(), s.Host, s.Provider, s.where(), s.Messages
		if !s.First.IsZero() && !s.Last.IsZero() {
			ref.Span = s.Last.Sub(s.First).Hours()
		}
	}
	return ref
}

// ---------------------------------------------------------------- gateway

// accountClasses labels each gateway account personal or work: its own label when it has one, otherwise the
// provider's highest-priority accounts are personal (the gateway fills personal first) and the rest work.
func accountClasses(accts []store.GatewayAccount) map[string]store.GatewayAccount {
	top := map[string]int{}
	for _, a := range accts {
		if p, ok := top[a.Provider]; !ok || a.Priority > p {
			top[a.Provider] = a.Priority
		}
	}
	out := map[string]store.GatewayAccount{}
	for _, a := range accts {
		l := strings.ToLower(a.Label)
		switch {
		case l == "personal" || l == "work":
		case a.Priority == top[a.Provider]:
			l = "personal"
		default:
			l = "work"
		}
		a.Label = l
		out[a.AuthIndex] = a
	}
	return out
}

type gwRow struct {
	ts                           time.Time
	auth, account, model         string
	input, output, cached, total int64
	latency                      int64
	failed                       bool
	code                         int
}

func queryGateway(ctx context.Context, db *sql.DB, from, to time.Time) ([]gwRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT ts, auth_index, account, model, input_tokens, output_tokens, cached_tokens,
		total_tokens, latency_ms, failed, status_code FROM gateway_requests WHERE ts >= ? AND ts < ?`, bound(from), bound(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gwRow
	for rows.Next() {
		var g gwRow
		var ts string
		var f int
		if err := rows.Scan(&ts, &g.auth, &g.account, &g.model, &g.input, &g.output, &g.cached, &g.total, &g.latency, &f, &g.code); err != nil {
			return nil, err
		}
		g.ts, g.failed = store.ParseT(ts), f != 0
		out = append(out, g)
	}
	return out, rows.Err()
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	return sorted[max(0, min(len(sorted)-1, i))]
}

func latencies(rows []gwRow) map[string][]float64 {
	out := map[string][]float64{}
	for _, g := range rows {
		if !g.failed && g.latency > 0 {
			out[g.model] = append(out[g.model], float64(g.latency))
		}
	}
	for _, v := range out {
		sort.Float64s(v)
	}
	return out
}

func (r *Report) buildGateway(ctx context.Context, db *sql.DB, accts []store.GatewayAccount, classes map[string]store.GatewayAccount) error {
	b := r.buckets
	rows, err := queryGateway(ctx, db, b.From, b.To)
	if err != nil {
		return err
	}
	l := &r.LLM
	l.GwErrorsByCode = map[int]int{}
	l.WorkHours = zeros(24)
	byAcct := newSet(b.N)
	byModel := newSet(b.N)
	errs := newSet(b.N)
	share := map[string]*AccountShare{}
	var workTok, allTok int64
	// Personal windows at >= 95% over time, to tell whether work was used because personal was capped.
	capped, err := personalCapTimes(ctx, db, classes, b.From)
	if err != nil {
		return err
	}
	for _, g := range rows {
		i := b.Index(g.ts)
		a, known := classes[g.auth]
		class := "unknown"
		name := g.account
		prov := ""
		if known {
			class, prov = a.Label, a.Provider
			if name == "" {
				name = a.Email
			}
		}
		label := class
		if prov != "" {
			label = prov + " · " + class
		}
		byAcct.add(label, i, 1)
		tk := GatewayTokens(g.model, g.input, g.output, g.cached, g.total)
		byModel.add(g.model, i, float64(tk.Total()))
		r.Cost.Gateway += Cost(g.model, tk)
		l.GwRequests++
		if g.failed {
			l.GwFailed++
			l.GwErrorsByCode[g.code]++
			errs.add("Failed", i, 1)
			if g.code == 429 {
				l.GwRateLimited++
			}
		} else {
			errs.add("OK", i, 1)
		}
		s := share[g.auth]
		if s == nil {
			s = &AccountShare{Account: name, Provider: prov, Class: class}
			share[g.auth] = s
		}
		s.Requests++
		s.Tokens += tk.Total()
		allTok += tk.Total()
		if class == "work" {
			workTok += tk.Total()
			l.WorkRequests++
			l.WorkHours[g.ts.In(b.Loc).Hour()]++
			if capped(prov, g.ts) {
				l.WorkWhileCapped++
			}
		}
	}
	l.GwRequestsByAcct = byAcct.series()
	l.GwTokensByModel = byModel.series()
	l.GwErrorSeries = errs.fixed([]string{"OK", "Failed"}, []string{"s0", "bad"})
	if allTok > 0 {
		l.WorkTokenPct = float64(workTok) * 100 / float64(allTok)
	}
	for _, s := range share {
		l.Share = append(l.Share, *s)
	}
	sort.Slice(l.Share, func(i, j int) bool { return l.Share[i].Tokens > l.Share[j].Tokens })
	cur := latencies(rows)
	var prev map[string][]float64
	if r.Range.Dur > 0 {
		p := b.Prev()
		prows, err := queryGateway(ctx, db, p.From, p.To)
		if err != nil {
			return err
		}
		prev = latencies(prows)
	}
	for m, v := range cur {
		ml := ModelLatency{Model: m, N: len(v), P50: percentile(v, 50), P95: percentile(v, 95)}
		if pv := prev[m]; len(pv) > 0 {
			ml.PrevP95, ml.PrevN = percentile(pv, 95), len(pv)
		}
		l.Latency = append(l.Latency, ml)
	}
	sort.Slice(l.Latency, func(i, j int) bool { return l.Latency[i].Model < l.Latency[j].Model })
	return nil
}

// capSpan is a stretch of time during which an account window sat at its cap.
type capSpan struct {
	from, to time.Time // to zero: still capped at the last observation, reset unknown
}

// capPoint is one quota observation of one account window.
type capPoint struct {
	at    time.Time
	used  float64
	reset int64 // unix seconds, 0 unknown
}

// capThreshold is the used % from which a window counts as capped.
const capThreshold = 95

// capSpans turns one window's observations (in time order) into capped intervals. An interval opens at a
// capped observation and closes at whichever comes first: the next uncapped observation, or the window's
// reset (a window that resets starts over even if no one looks at it until much later).
func capSpans(pts []capPoint) []capSpan {
	var out []capSpan
	var open *capSpan
	var reset time.Time
	closeAt := func(t time.Time) {
		if !reset.IsZero() && reset.Before(t) {
			t = reset
		}
		open.to = t
		out = append(out, *open)
		open = nil
	}
	for _, p := range pts {
		if open != nil && !reset.IsZero() && !p.at.Before(reset) {
			closeAt(reset)
		}
		if p.used >= capThreshold {
			if open == nil {
				open = &capSpan{from: p.at}
			}
			reset = time.Time{}
			if p.reset > 0 {
				reset = time.Unix(p.reset, 0)
			}
			continue
		}
		if open != nil {
			closeAt(p.at)
		}
	}
	if open != nil {
		open.to = reset // zero when the reset is unknown: capped until told otherwise
		out = append(out, *open)
	}
	return out
}

// personalCapTimes returns a test for "a personal account of this provider had a window at its cap at t",
// from the quota history.
func personalCapTimes(ctx context.Context, db *sql.DB, classes map[string]store.GatewayAccount, from time.Time) (func(string, time.Time) bool, error) {
	personal := map[string]string{} // account id -> provider
	for _, a := range classes {
		if a.Label == "personal" {
			personal[a.ID] = a.Provider
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT account_id, win, at, used_pct, reset_at FROM gateway_quota_history WHERE at >= ?
		ORDER BY account_id, win, at`, bound(from.Add(-7*24*time.Hour)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	series := map[string][]capPoint{}
	provOf := map[string]string{}
	for rows.Next() {
		var id, win, at string
		var p capPoint
		if err := rows.Scan(&id, &win, &at, &p.used, &p.reset); err != nil {
			return nil, err
		}
		prov, ok := personal[id]
		if !ok {
			continue
		}
		p.at = store.ParseT(at)
		k := id + "\x00" + win
		series[k] = append(series[k], p)
		provOf[k] = prov
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	spans := map[string][]capSpan{} // provider -> capped intervals
	for k, pts := range series {
		spans[provOf[k]] = append(spans[provOf[k]], capSpans(pts)...)
	}
	return func(prov string, t time.Time) bool {
		for _, s := range spans[prov] {
			if !t.Before(s.from) && (s.to.IsZero() || t.Before(s.to)) {
				return true
			}
		}
		return false
	}, nil
}

// ---------------------------------------------------------------- quota

func windowLength(name string, minutes int64) time.Duration {
	if minutes > 0 {
		return time.Duration(minutes) * time.Minute
	}
	n := strings.ToLower(name)
	if d, err := time.ParseDuration(n); err == nil && d > 0 {
		return d
	}
	if strings.HasSuffix(n, "d") {
		if days, err := strconv.Atoi(strings.TrimSuffix(n, "d")); err == nil {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	return 0
}

// project fills the burn rate, run-out time and verdict of q from its history points (same window
// instance only) or, without history, from the average rate since the window opened.
func project(q *QuotaView, pts []quotaPoint, length time.Duration, now time.Time) {
	var inWin []quotaPoint
	for _, p := range pts {
		if q.ResetAt.IsZero() || p.reset == q.ResetAt.Unix() {
			inWin = append(inWin, p)
		}
	}
	if len(inWin) >= 2 {
		a, z := inWin[0], inWin[len(inWin)-1]
		if h := z.at.Sub(a.at).Hours(); h >= 1.0/6 {
			q.BurnPerHour = max(0, (z.used-a.used)/h)
			q.BurnFrom = "history"
		}
	}
	if q.BurnFrom == "" && length > 0 && !q.ResetAt.IsZero() {
		opened := q.ResetAt.Add(-length)
		if el := now.Sub(opened).Hours(); el > 0.1 {
			q.BurnPerHour = q.UsedPct / el
			q.BurnFrom = "average"
		}
	}
	left := 100 - q.UsedPct
	switch {
	case q.UsedPct >= 100 || q.Exhausted:
		q.Verdict = "exhausted"
		if !q.ResetAt.IsZero() {
			q.Verdict += " until " + q.ResetAt.Local().Format("Mon 15:04")
		}
	case q.BurnPerHour <= 0:
		q.Verdict = "not being used"
	default:
		out := now.Add(time.Duration(left / q.BurnPerHour * float64(time.Hour)))
		if !q.ResetAt.IsZero() && !out.Before(q.ResetAt) {
			q.Verdict = "lasts until reset at this rate"
		} else {
			q.RunsOutAt = out
			q.Verdict = "runs out " + out.Local().Format("Mon 15:04") + " at this rate"
		}
	}
}

type quotaPoint struct {
	at    time.Time
	used  float64
	reset int64
}

func (r *Report) buildQuota(ctx context.Context, st *store.Store, accts []store.GatewayAccount, classes map[string]store.GatewayAccount, now time.Time) error {
	b := r.buckets
	hist := map[string][]quotaPoint{}
	rows, err := st.DB.QueryContext(ctx, `SELECT account_id, win, at, used_pct, reset_at FROM gateway_quota_history
		WHERE at >= ? ORDER BY at`, bound(b.From.Add(-8*24*time.Hour)))
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, win, at string
		var p quotaPoint
		if err := rows.Scan(&id, &win, &at, &p.used, &p.reset); err != nil {
			rows.Close()
			return err
		}
		p.at = store.ParseT(at)
		hist[id+"\x00"+win] = append(hist[id+"\x00"+win], p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range accts {
		class := classes[a.AuthIndex].Label
		for _, w := range a.Windows {
			q := QuotaView{ID: fmt.Sprintf("q-%s-%d-%s", a.Provider, a.Priority, w.Name), Account: a.Email, Provider: a.Provider,
				Class: class, Window: w.Name, UsedPct: w.UsedPct, Observed: store.ParseT(a.QuotaAt), Serving: a.Serving,
				Exhausted: w.Status == "rejected"}
			if w.ResetAt > 0 {
				q.ResetAt = time.Unix(w.ResetAt, 0)
			}
			// A window whose reset has passed has started over; the stored percentage is stale.
			if !q.ResetAt.IsZero() && now.After(q.ResetAt) {
				q.UsedPct, q.Exhausted = 0, false
				q.Verdict = "reset; waiting for the next request"
			}
			pts := hist[a.ID+"\x00"+w.Name]
			q.History = zeros(b.N)
			last := -1.0
			pi := 0
			for i := 0; i < b.N; i++ {
				end := b.BucketStart(i + 1)
				for pi < len(pts) && pts[pi].at.Before(end) {
					last = pts[pi].used
					pi++
				}
				if last >= 0 {
					q.History[i] = last
				}
			}
			q.HasHistory = len(pts) >= 2
			if q.Verdict == "" {
				project(&q, pts, windowLength(w.Name, 0), now)
			}
			r.Quota = append(r.Quota, q)
		}
	}
	// Codex rollouts report their own limits per host (no account identity).
	obs, err := st.Quotas(ctx)
	if err != nil {
		return err
	}
	for _, o := range obs {
		add := func(kind string, used float64, minutes, reset int64) {
			q := QuotaView{ID: fmt.Sprintf("q-%s-%s-%s-%s", o.Provider, o.Host, o.LimitID, kind), Account: o.Host + " (" + o.LimitID + ")",
				Provider: o.Provider, Class: "session", Window: fmtWindow(minutes, kind), UsedPct: used, Observed: store.ParseT(o.ObservedAt)}
			if reset > 0 {
				q.ResetAt = time.Unix(reset, 0)
			}
			if !q.ResetAt.IsZero() && now.After(q.ResetAt) {
				q.UsedPct, q.Verdict = 0, "reset; waiting for the next observation"
			} else {
				project(&q, nil, windowLength("", minutes), now)
			}
			q.History = zeros(b.N)
			r.Quota = append(r.Quota, q)
		}
		if p := o.Primary; p != nil {
			add("primary", p.Used, p.Minutes, p.Reset)
		}
		if s := o.Secondary; s != nil {
			add("secondary", s.Used, s.Minutes, s.Reset)
		}
	}
	return nil
}

func fmtWindow(minutes int64, fallback string) string {
	switch {
	case minutes <= 0:
		return fallback
	case minutes%1440 == 0:
		return fmt.Sprintf("%dd", minutes/1440)
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dm", minutes)
}

// ---------------------------------------------------------------- sessions

type actRow struct {
	hour            time.Time
	session         int64
	blocks, prompts int64
}

// queryActivity returns the activity hours whose midpoint falls in [from, to). Activity is kept per hour, so an
// hour cut by a window edge counts in exactly one of the two windows on either side.
func queryActivity(ctx context.Context, db *sql.DB, from, to time.Time) ([]actRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT hour, session_id, blocks, prompts FROM activity WHERE hour >= ? AND hour <= ?`,
		hourKey(from.Add(-time.Hour)), hourKey(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []actRow
	for rows.Next() {
		var a actRow
		var h string
		if err := rows.Scan(&h, &a.session, &a.blocks, &a.prompts); err != nil {
			return nil, err
		}
		a.hour = parseHour(h)
		if m := hourMid(a.hour); m.Before(from) || !m.Before(to) {
			continue
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func (r *Report) buildSessions(ctx context.Context, db *sql.DB, sess *sessIndex) error {
	b := r.buckets
	v := &r.Sessions
	acts, err := queryActivity(ctx, db, b.From, b.To)
	if err != nil {
		return err
	}
	blocks := newSet(b.N)
	prompts := newSet(b.N)
	hostSpark := map[string][]float64{}
	proj := map[string]float64{}
	activeHours := map[int64]float64{}
	for _, a := range acts {
		i := b.Index(hourMid(a.hour))
		if i < 0 {
			continue
		}
		s := sess.byID[a.session]
		host, prov, where := "?", "?", ""
		if s != nil {
			host, prov, where = s.Host, s.Provider, s.where()
		}
		blocks.add(host, i, float64(a.blocks))
		prompts.add(prov, i, float64(a.prompts))
		if hostSpark[host] == nil {
			hostSpark[host] = zeros(b.N)
		}
		hostSpark[host][i] += float64(a.blocks)
		v.Blocks += a.blocks
		v.Prompts += a.prompts
		if where != "" {
			proj[where] += float64(a.blocks)
		}
		lt := a.hour.In(b.Loc)
		wd := (int(lt.Weekday()) + 6) % 7
		v.Heat[wd][lt.Hour()] += float64(a.prompts)
		root := sess.root[a.session]
		if root == 0 {
			root = a.session
		}
		if s != nil && s.Parent == "" {
			activeHours[root]++
		}
	}
	v.BlocksByHost = blocks.series()
	v.PromptsByProvider = prompts.series()
	v.HostSpark = hostSpark
	peak, pd, ph := 0.0, 0, 0
	for d := range v.Heat {
		for h := range v.Heat[d] {
			if v.Heat[d][h] > peak {
				peak, pd, ph = v.Heat[d][h], d, h
			}
		}
	}
	if peak > 0 {
		v.HeatPeak = fmt.Sprintf("%s %02d:00", []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}[pd], ph)
	}
	for k, n := range proj {
		v.Projects = append(v.Projects, charts.Bar{Label: k, Value: n, Href: "/sessions?project=" + urlQuery(k)})
	}
	sort.Slice(v.Projects, func(i, j int) bool { return v.Projects[i].Value > v.Projects[j].Value })
	v.Projects = v.Projects[:min(len(v.Projects), 10)]

	started := newSet(b.N)
	var spans []float64
	var hours []float64
	var refs []SessionRef
	for id, s := range sess.byID {
		if s.Parent != "" || s.Messages == 0 {
			continue
		}
		if i := b.Index(s.First); i >= 0 {
			started.add(s.Host+" · "+s.Provider, i, 1)
			v.Started++
			if !s.Last.IsZero() {
				spans = append(spans, s.Last.Sub(s.First).Minutes())
			}
		}
		if h := activeHours[id]; h > 0 {
			hours = append(hours, h)
			ref := sessionRef(sess, id)
			ref.Hours = h
			refs = append(refs, *ref)
		}
	}
	v.StartedByHost = started.series()
	v.MedianSpanMin = median(spans)
	v.MedianActiveH = median(hours)
	if len(hours) > 0 {
		v.AvgActiveH = sum(hours) / float64(len(hours))
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Hours != refs[j].Hours {
			return refs[i].Hours > refs[j].Hours
		}
		return refs[i].Messages > refs[j].Messages
	})
	v.Longest = refs[:min(len(refs), 8)]

	// Tools
	trows, err := db.QueryContext(ctx, `SELECT tool, COUNT(*), SUM(is_error) FROM tool_calls WHERE ts >= ? AND ts < ? GROUP BY tool`,
		bound(b.From), bound(b.To))
	if err != nil {
		return err
	}
	for trows.Next() {
		var t ToolStat
		if err := trows.Scan(&t.Tool, &t.Calls, &t.Errors); err != nil {
			trows.Close()
			return err
		}
		v.Tools = append(v.Tools, t)
		v.ToolCalls += t.Calls
		v.ToolErrors += t.Errors
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return err
	}
	sort.Slice(v.Tools, func(i, j int) bool { return v.Tools[i].Calls > v.Tools[j].Calls })

	if r.Range.Dur > 0 {
		p := b.Prev()
		pacts, err := queryActivity(ctx, db, p.From, p.To)
		if err != nil {
			return err
		}
		for _, a := range pacts {
			v.PrevPrompts += a.prompts
		}
		for _, s := range sess.byID {
			if s.Parent == "" && s.Messages > 0 && p.In(s.First) {
				v.PrevStarted++
			}
		}
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_calls WHERE ts >= ? AND ts < ?`, bound(p.From), bound(p.To)).Scan(&v.PrevToolCalls)
	}
	return nil
}

func urlQuery(s string) string {
	r := strings.NewReplacer("%", "%25", "&", "%26", "+", "%2B", " ", "%20", "#", "%23", "?", "%3F")
	return r.Replace(s)
}

// ---------------------------------------------------------------- fleet

var (
	redRe      = regexp.MustCompile(`\bred=(\d+)`)
	optionalRe = regexp.MustCompile(`\boptional=(\d+)`)
	owaspRe    = regexp.MustCompile(`\bA(0[1-9]|10)\b(?::\d{4})?`)
	repoRe     = regexp.MustCompile(`github\.com/([^/\s]+/[^/\s]+)/(?:pull|issues)/\d+`)
)

// owaspNames labels the OWASP Top 10 (2021) categories reviewers cite as A01..A10.
var owaspNames = map[string]string{
	"A01": "A01 access control", "A02": "A02 cryptography", "A03": "A03 injection", "A04": "A04 insecure design",
	"A05": "A05 misconfiguration", "A06": "A06 vulnerable components", "A07": "A07 authentication",
	"A08": "A08 integrity", "A09": "A09 logging", "A10": "A10 SSRF",
}

type runRow struct {
	id                                 int64
	key, kind, target, status, summary string
	started, ended                     time.Time
}

func (r *Report) buildFleet(ctx context.Context, db *sql.DB) error {
	b := r.buckets
	f := &r.Fleet
	rows, err := db.QueryContext(ctx, `SELECT id, key, kind, target, status, summary, started_at, ended_at FROM runs
		WHERE started_at >= ? AND started_at < ? ORDER BY started_at`, bound(b.From), bound(b.To))
	if err != nil {
		return err
	}
	var runs []runRow
	for rows.Next() {
		var x runRow
		var s, e string
		if err := rows.Scan(&x.id, &x.key, &x.kind, &x.target, &x.status, &x.summary, &s, &e); err != nil {
			rows.Close()
			return err
		}
		x.started, x.ended = store.ParseT(s), store.ParseT(e)
		runs = append(runs, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	byKind := newSet(b.N)
	byOutcome := newSet(b.N)
	verdicts := newSet(b.N)
	kinds := map[string]*KindStat{}
	durs := map[string][]float64{}
	repos := map[string]float64{}
	blockers := map[string]float64{}
	cycles := map[string]*Cycle{}
	var blockerRuns []int64
	for _, x := range runs {
		i := b.Index(x.started)
		f.Runs++
		byKind.add(x.kind, i, 1)
		byOutcome.add(x.status, i, 1)
		k := kinds[x.kind]
		if k == nil {
			k = &KindStat{Kind: x.kind}
			kinds[x.kind] = k
		}
		k.Runs++
		switch x.status {
		case "succeeded":
			k.Succeeded++
		case "failed":
			k.Failed++
			f.Failed++
		case "running":
			k.Running++
		default:
			k.Exited++
		}
		if !x.ended.IsZero() {
			durs[x.kind] = append(durs[x.kind], x.ended.Sub(x.started).Minutes())
		}
		if m := repoRe.FindStringSubmatch(x.target); m != nil {
			repos[m[1]]++
		}
		if x.kind == "review" {
			switch v := reviewVerdict(x.summary); v {
			case "APPROVE":
				f.Approve++
				verdicts.add("Approve", i, 1)
			case "COMMENT", "REQUEST_CHANGES":
				f.Comment++
				verdicts.add("Comment", i, 1)
			default:
				f.OtherVerdict++
				verdicts.add("No verdict", i, 1)
			}
			red := atoi(redRe.FindStringSubmatch(x.summary))
			f.Red += red
			f.Optional += atoi(optionalRe.FindStringSubmatch(x.summary))
			for _, c := range owaspRe.FindAllStringSubmatch(x.summary, -1) {
				blockers[owaspNames["A"+c[1]]]++
			}
			if red > 0 {
				blockerRuns = append(blockerRuns, x.id)
			}
		}
		target := x.target
		if target == "" {
			target = x.key
		}
		c := cycles[target]
		if c == nil {
			c = &Cycle{Target: x.target, Key: x.key, Kind: x.kind}
			cycles[target] = c
		}
		c.Runs++
		c.LastRun = x.id
		end := x.ended
		if end.IsZero() {
			end = r.Generated
		}
		c.BusyMin += end.Sub(x.started).Minutes()
		if c.first.IsZero() || x.started.Before(c.first) {
			c.first = x.started
		}
		c.SpanMin = max(c.SpanMin, end.Sub(c.first).Minutes())
	}
	// Blocker categories the summary line doesn't name: look in the review's own log (small).
	if len(blockers) == 0 {
		for _, id := range blockerRuns {
			var lg string
			if err := db.QueryRowContext(ctx, `SELECT substr(log, 1, 200000) FROM runs WHERE id = ?`, id).Scan(&lg); err != nil {
				continue
			}
			seen := map[string]bool{}
			for _, c := range owaspRe.FindAllStringSubmatch(lg, -1) {
				if n := owaspNames["A"+c[1]]; n != "" && !seen[n] {
					seen[n] = true
					blockers[n]++
				}
			}
		}
	}
	f.ByKind = byKind.fixed([]string{"issue", "pr", "review", "other"}, []string{"s0", "s1", "s2", "s3"})
	f.ByOutcome = byOutcome.fixed([]string{"succeeded", "exited", "running", "failed"}, []string{"ok", "s0", "warn", "bad"})
	f.Verdicts = verdicts.fixed([]string{"Approve", "Comment", "No verdict"}, []string{"ok", "warn", "mut"})
	for name, k := range kinds {
		k.MedianMin = median(durs[name])
		f.Kinds = append(f.Kinds, *k)
	}
	sort.Slice(f.Kinds, func(i, j int) bool { return f.Kinds[i].Runs > f.Kinds[j].Runs })
	for k, n := range repos {
		f.Repos = append(f.Repos, charts.Bar{Label: k, Value: n, Href: "https://github.com/" + k})
	}
	sort.Slice(f.Repos, func(i, j int) bool {
		return f.Repos[i].Value > f.Repos[j].Value || f.Repos[i].Value == f.Repos[j].Value && f.Repos[i].Label < f.Repos[j].Label
	})
	f.Repos = f.Repos[:min(len(f.Repos), 10)]
	for k, n := range blockers {
		f.Blockers = append(f.Blockers, charts.Bar{Label: k, Value: n, Class: "bad"})
	}
	sort.Slice(f.Blockers, func(i, j int) bool {
		return f.Blockers[i].Value > f.Blockers[j].Value || f.Blockers[i].Value == f.Blockers[j].Value && f.Blockers[i].Label < f.Blockers[j].Label
	})
	var spans []float64
	for _, c := range cycles {
		f.Cycles = append(f.Cycles, *c)
		if c.Kind != "review" {
			spans = append(spans, c.SpanMin)
		}
	}
	sort.Slice(f.Cycles, func(i, j int) bool { return f.Cycles[i].SpanMin > f.Cycles[j].SpanMin })
	f.Cycles = f.Cycles[:min(len(f.Cycles), 10)]
	f.MedianCycleMin = median(spans)
	if r.Range.Dur > 0 {
		p := b.Prev()
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE started_at >= ? AND started_at < ?`, bound(p.From), bound(p.To)).Scan(&f.PrevRuns)
	}
	return nil
}

func atoi(m []string) int {
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// reviewVerdict reads the verdict of a "REVIEWED <url> <verdict> <detail>" summary line.
func reviewVerdict(summary string) string {
	f := strings.Fields(summary)
	for i, w := range f {
		if w == "REVIEWED" && i+2 < len(f) {
			return strings.ToUpper(f[i+2])
		}
	}
	return ""
}

// ---------------------------------------------------------------- chat

func (r *Report) buildChat(ctx context.Context, db *sql.DB) error {
	b := r.buckets
	c := &r.Chat
	var me string
	_ = db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'slack_user_id'`).Scan(&me)
	rows, err := db.QueryContext(ctx, `SELECT at, conv_id, conv_name, conv_kind, user_id, user_name, mentions_me, thread_ts
		FROM chat_messages WHERE at >= ? AND at < ?`, bound(b.From), bound(b.To))
	if err != nil {
		return err
	}
	defer rows.Close()
	byConv := newSet(b.N)
	toMe := newSet(b.N)
	people := map[string]float64{}
	threads := map[string]*Thread{}
	tpeople := map[string]map[string]bool{}
	for rows.Next() {
		var at, convID, conv, kind, uid, user, tts string
		var mention int
		if err := rows.Scan(&at, &convID, &conv, &kind, &uid, &user, &mention, &tts); err != nil {
			return err
		}
		c.Total++
		i := b.Index(store.ParseT(at))
		label := conv
		if kind == "channel" || kind == "private" {
			label = "#" + conv
		}
		byConv.add(label, i, 1)
		if user == "" {
			user = uid
		}
		if uid != me || me == "" {
			people[user]++
		}
		if mention != 0 {
			c.Mentions++
			toMe.add("Mentions", i, 1)
		}
		if (kind == "dm" || kind == "group_dm") && uid != me {
			c.DMs++
			toMe.add("DMs", i, 1)
		}
		if tts != "" {
			k := convID + "\x00" + tts
			t := threads[k]
			if t == nil {
				t = &Thread{Conv: label, ConvID: convID, TS: tts}
				threads[k] = t
				tpeople[k] = map[string]bool{}
			}
			t.Replies++
			tpeople[k][uid] = true
			if at > t.Last {
				t.Last = at
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	c.Enabled = c.Total > 0
	c.ByConv = byConv.series()
	c.ToMe = toMe.fixed([]string{"Mentions", "DMs"}, []string{"s4", "s6"})
	for p, n := range people {
		c.People = append(c.People, charts.Bar{Label: p, Value: n})
	}
	sort.Slice(c.People, func(i, j int) bool {
		return c.People[i].Value > c.People[j].Value || c.People[i].Value == c.People[j].Value && c.People[i].Label < c.People[j].Label
	})
	c.People = c.People[:min(len(c.People), 10)]
	for k, t := range threads {
		t.People = len(tpeople[k])
		c.Threads = append(c.Threads, *t)
	}
	sort.Slice(c.Threads, func(i, j int) bool {
		return c.Threads[i].Replies > c.Threads[j].Replies || c.Threads[i].Replies == c.Threads[j].Replies && c.Threads[i].Last > c.Threads[j].Last
	})
	c.Threads = c.Threads[:min(len(c.Threads), 8)]
	return nil
}

// ---------------------------------------------------------------- KPIs

func (r *Report) buildKPIs() {
	n := r.buckets.N
	hasPrev := r.Range.Dur > 0
	tok := totals(r.LLM.TokensByModel, n)
	prompts := totals(r.Sessions.PromptsByProvider, n)
	runs := totals(r.Fleet.ByKind, n)
	gw := totals(r.LLM.GwRequestsByAcct, n)
	cost := totals(r.Cost.Series, n)
	r.KPIs = []KPI{
		{ID: "tokens", Label: "Tokens (transcripts)", Value: float64(r.LLM.Totals.Total()), Prev: float64(r.LLM.PrevTotal), Unit: "count", UpGood: true, Spark: tok, Href: "#llm"},
		{ID: "value", Label: "API-equivalent value", Value: r.Cost.Total, Prev: r.Cost.Prev, Unit: "dollars", UpGood: true, Spark: cost, Href: "#cost"},
		{ID: "prompts", Label: "Prompts you typed", Value: float64(r.Sessions.Prompts), Prev: float64(r.Sessions.PrevPrompts), Unit: "count", UpGood: true, Spark: prompts, Href: "#sessions"},
		{ID: "sessions", Label: "Sessions started", Value: float64(r.Sessions.Started), Prev: float64(r.Sessions.PrevStarted), Unit: "count", UpGood: true, Spark: totals(r.Sessions.StartedByHost, n), Href: "#sessions"},
		{ID: "runs", Label: "Fleet runs", Value: float64(r.Fleet.Runs), Prev: float64(r.Fleet.PrevRuns), Unit: "count", UpGood: true, Spark: runs, Href: "#fleet"},
		{ID: "gateway", Label: "Gateway requests", Value: float64(r.LLM.GwRequests), Unit: "count", UpGood: true, Spark: gw, Href: "#gateway"},
	}
	for i := range r.KPIs {
		r.KPIs[i].HasPrev = hasPrev && r.KPIs[i].ID != "gateway"
	}
}
