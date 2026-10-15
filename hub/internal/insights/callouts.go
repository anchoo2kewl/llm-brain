package insights

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Callout is one computed finding in plain English. Href points at the chart it comes from.
type Callout struct {
	ID    string `json:"id"`
	Level string `json:"level"` // bad | warn | good | info
	Text  string `json:"text"`
	Href  string `json:"href"`
}

// rule inspects a report and returns zero or more findings. Rules only read numbers already in the report.
type rule func(r *Report) []Callout

var rules = []rule{
	ruleQuota, ruleGatewayErrors, ruleFleetFailures, ruleWorkShare, ruleLatency, ruleReviews, ruleToolErrors,
	ruleValue, ruleTokenTrend, ruleCache, ruleRhythm, ruleChat, ruleRollup,
}

var levelOrder = map[string]int{"bad": 0, "warn": 1, "good": 2, "info": 3}

// Callouts runs every rule, most urgent first.
func Callouts(r *Report) []Callout {
	var out []Callout
	for _, f := range rules {
		out = append(out, f(r)...)
	}
	sort.SliceStable(out, func(i, j int) bool { return levelOrder[out[i].Level] < levelOrder[out[j].Level] })
	return out
}

func period(r *Report) string {
	if r.Range.Dur == 0 {
		return "overall"
	}
	return "in the last " + r.Range.Label
}

func prevPeriod(r *Report) string { return "the previous " + r.Range.Label }

func pct(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a * 100 / b
}

func clockAt(t time.Time) string {
	if time.Until(t) < 20*time.Hour && t.Day() == time.Now().Day() {
		return t.Local().Format("15:04")
	}
	return t.Local().Format("Mon 15:04")
}

func ruleQuota(r *Report) []Callout {
	var out []Callout
	for _, q := range r.Quota {
		name := q.Provider + " " + q.Class + " " + q.Window + " window"
		switch {
		case q.Exhausted || q.UsedPct >= 100:
			t := fmt.Sprintf("The %s is exhausted", name)
			if !q.ResetAt.IsZero() {
				t += "; it resets " + clockAt(q.ResetAt)
			}
			out = append(out, Callout{ID: "quota-" + q.ID, Level: "bad", Text: t + ".", Href: "#" + q.ID})
		case !q.RunsOutAt.IsZero():
			lvl := "warn"
			if time.Until(q.RunsOutAt) < 2*time.Hour {
				lvl = "bad"
			}
			t := fmt.Sprintf("The %s runs out around %s at the current rate (%.0f%% used, %.1f%%/h)", name, clockAt(q.RunsOutAt), q.UsedPct, q.BurnPerHour)
			if !q.ResetAt.IsZero() {
				t += ", before its reset at " + clockAt(q.ResetAt)
			}
			out = append(out, Callout{ID: "quota-" + q.ID, Level: lvl, Text: t + ".", Href: "#" + q.ID})
		}
	}
	return out
}

func ruleGatewayErrors(r *Report) []Callout {
	l := r.LLM
	if l.GwRequests == 0 || l.GwFailed < 3 || pct(float64(l.GwFailed), float64(l.GwRequests)) < 5 {
		return nil
	}
	t := fmt.Sprintf("%.0f%% of gateway requests failed %s (%d of %d)", pct(float64(l.GwFailed), float64(l.GwRequests)), period(r), l.GwFailed, l.GwRequests)
	if l.GwRateLimited > 0 {
		t += fmt.Sprintf("; %d were 429 rate limits", l.GwRateLimited)
	}
	return []Callout{{ID: "gw-errors", Level: "bad", Text: t + ".", Href: "#gateway"}}
}

func ruleFleetFailures(r *Report) []Callout {
	f := r.Fleet
	if f.Runs < 3 || f.Failed == 0 || pct(float64(f.Failed), float64(f.Runs)) < 20 {
		return nil
	}
	return []Callout{{ID: "fleet-failures", Level: "bad", Href: "#fleet",
		Text: fmt.Sprintf("%d of %d fleet runs failed %s.", f.Failed, f.Runs, period(r))}}
}

// hourSpan describes when work requests happened: "all between 14:00 and 17:00" when they sit in one
// stretch of at most 10 hours, otherwise the busiest hour.
func hourSpan(hours []float64) string {
	first, last := -1, -1
	peak, ph := 0.0, 0
	for h, v := range hours {
		if v > 0 {
			if first < 0 {
				first = h
			}
			last = h
		}
		if v > peak {
			peak, ph = v, h
		}
	}
	if first < 0 {
		return ""
	}
	if last-first < 10 {
		return fmt.Sprintf("all between %02d:00 and %02d:00", first, (last+1)%24)
	}
	return fmt.Sprintf("most around %02d:00", ph)
}

func ruleWorkShare(r *Report) []Callout {
	l := r.LLM
	if l.GwRequests == 0 {
		return nil
	}
	if l.WorkRequests == 0 {
		return []Callout{{ID: "work-share", Level: "good", Href: "#share",
			Text: fmt.Sprintf("Personal accounts served every gateway request %s (%d); work accounts were not needed.", period(r), l.GwRequests)}}
	}
	t := fmt.Sprintf("Work accounts served %.0f%% of gateway tokens %s (%d requests), %s", l.WorkTokenPct, period(r), l.WorkRequests, hourSpan(l.WorkHours))
	switch {
	case l.WorkWhileCapped == l.WorkRequests:
		t += ", every one while a personal window was at its cap"
	case l.WorkWhileCapped > 0:
		t += fmt.Sprintf(", %d of them while a personal window was at its cap", l.WorkWhileCapped)
	}
	lvl := "info"
	if l.WorkTokenPct >= 25 {
		lvl = "warn"
	}
	return []Callout{{ID: "work-share", Level: lvl, Text: t + ".", Href: "#share"}}
}

func ruleLatency(r *Report) []Callout {
	var out []Callout
	for _, m := range r.LLM.Latency {
		if m.N < 5 || m.PrevN < 5 || m.PrevP95 <= 0 {
			continue
		}
		ratio := m.P95 / m.PrevP95
		switch {
		case ratio >= 1.5:
			out = append(out, Callout{ID: "latency-" + m.Model, Level: "warn", Href: "#latency",
				Text: fmt.Sprintf("%s latency p95 rose %.1f× vs %s (%s → %s).", m.Model, ratio, prevPeriod(r), secs(m.PrevP95), secs(m.P95))})
		case ratio <= 0.67:
			out = append(out, Callout{ID: "latency-" + m.Model, Level: "good", Href: "#latency",
				Text: fmt.Sprintf("%s latency p95 fell to %s from %s in %s.", m.Model, secs(m.P95), secs(m.PrevP95), prevPeriod(r))})
		}
	}
	return out
}

func secs(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	return fmt.Sprintf("%.0fms", ms)
}

func ruleReviews(r *Report) []Callout {
	f := r.Fleet
	n := f.Approve + f.Comment
	if n == 0 {
		return nil
	}
	t := fmt.Sprintf("PR reviews approve %.0f%% (%d of %d) %s", pct(float64(f.Approve), float64(n)), f.Approve, n, period(r))
	switch {
	case len(f.Blockers) > 0:
		t += "; the most common blocker is " + f.Blockers[0].Label
	case f.Red > 0:
		t += fmt.Sprintf("; %d red findings, %d optional", f.Red, f.Optional)
	case f.Optional > 0:
		t += fmt.Sprintf("; no red findings, %d optional suggestions", f.Optional)
	}
	lvl := "info"
	if pct(float64(f.Approve), float64(n)) < 50 {
		lvl = "warn"
	}
	return []Callout{{ID: "reviews", Level: lvl, Text: t + ".", Href: "#reviews"}}
}

func ruleToolErrors(r *Report) []Callout {
	var worst ToolStat
	worstRate := 0.0
	for _, t := range r.Sessions.Tools {
		if t.Calls < 20 {
			continue
		}
		if rate := float64(t.Errors) / float64(t.Calls); rate > worstRate {
			worst, worstRate = t, rate
		}
	}
	if worstRate < 0.15 {
		return nil
	}
	return []Callout{{ID: "tool-errors", Level: "warn", Href: "#tools",
		Text: fmt.Sprintf("%s calls fail %.0f%% of the time %s (%d of %d).", worst.Tool, worstRate*100, period(r), worst.Errors, worst.Calls)}}
}

func ruleValue(r *Report) []Callout {
	if r.Cost.Total < 0.01 {
		return nil
	}
	t := fmt.Sprintf("Subscriptions delivered about %s of API-equivalent usage %s (estimate)", dollars(r.Cost.Total), period(r))
	if r.Range.Dur > 0 && r.Cost.Prev > 0 {
		t += ", " + change(r.Cost.Total, r.Cost.Prev, prevPeriod(r))
	}
	return []Callout{{ID: "value", Level: "good", Text: t + ".", Href: "#cost"}}
}

func dollars(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("$%.0f", math.Round(v))
	}
	return fmt.Sprintf("$%.2f", v)
}

// change phrases a against b over the previous period: "up 40% vs the previous 7 days", "3× the previous 7
// days", "level with the previous 7 days".
func change(a, b float64, prev string) string {
	d := (a - b) / b * 100
	switch {
	case a/b >= 3:
		return fmt.Sprintf("%.0f× %s", a/b, prev)
	case d >= 1:
		return fmt.Sprintf("up %.0f%% vs %s", d, prev)
	case d <= -1:
		return fmt.Sprintf("down %.0f%% vs %s", -d, prev)
	}
	return "level with " + prev
}

func ruleTokenTrend(r *Report) []Callout {
	if r.Range.Dur == 0 || r.LLM.PrevTotal == 0 {
		return nil
	}
	cur, prev := float64(r.LLM.Totals.Total()), float64(r.LLM.PrevTotal)
	d := (cur - prev) / prev * 100
	if math.Abs(d) < 50 {
		return nil
	}
	return []Callout{{ID: "token-trend", Level: "info", Href: "#llm",
		Text: fmt.Sprintf("Token use is %s.", change(cur, prev, prevPeriod(r)))}}
}

func ruleCache(r *Report) []Callout {
	if r.LLM.Totals.Total() == 0 || r.LLM.CacheShare < 80 {
		return nil
	}
	return []Callout{{ID: "cache", Level: "info", Href: "#llm",
		Text: fmt.Sprintf("%.0f%% of input tokens were cache reads %s, the cheapest kind.", r.LLM.CacheShare, period(r))}}
}

func ruleRhythm(r *Report) []Callout {
	if r.Sessions.HeatPeak == "" || r.Sessions.Prompts < 10 {
		return nil
	}
	return []Callout{{ID: "rhythm", Level: "info", Href: "#heat",
		Text: fmt.Sprintf("You typed %d prompts %s; the busiest hour was %s.", r.Sessions.Prompts, period(r), r.Sessions.HeatPeak)}}
}

func ruleChat(r *Report) []Callout {
	c := r.Chat
	if c.Mentions+c.DMs == 0 {
		return nil
	}
	t := fmt.Sprintf("%d mentions and %d direct messages to you %s", c.Mentions, c.DMs, period(r))
	if len(c.Threads) > 0 && c.Threads[0].Replies >= 5 {
		t += fmt.Sprintf("; the busiest thread (%s) has %d messages", c.Threads[0].Conv, c.Threads[0].Replies)
	}
	return []Callout{{ID: "chat", Level: "info", Text: t + ".", Href: "#chat"}}
}

func ruleRollup(r *Report) []Callout {
	if r.RollupPct >= 100 {
		return nil
	}
	return []Callout{{ID: "rollup", Level: "info", Href: "#llm",
		Text: fmt.Sprintf("Still reading history (%d%%): token, prompt and tool numbers are partial until it finishes.", r.RollupPct)}}
}
