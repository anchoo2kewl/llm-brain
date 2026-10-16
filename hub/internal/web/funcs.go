package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Status is bin/fleet's status.json snapshot (written by `fleet snapshot` every tick).
type Status struct {
	Host   string `json:"host"`
	At     string `json:"at"`
	Paused bool   `json:"paused"`
	Roles  struct {
		Implementer      string `json:"implementer"`
		InternalReviewer string `json:"internal_reviewer"`
		PRReviewer       string `json:"pr_reviewer"`
	} `json:"roles"`
	MaxInFlight int      `json:"max_in_flight"`
	Owned       []Issue  `json:"owned"`
	Queue       []Issue  `json:"queue"`
	PRs         []PR     `json:"prs"`
	Reviews     []Review `json:"reviews"`
	Workers     []Worker `json:"workers"`
	Log         string   `json:"log"`
	Chat        *Chat    `json:"chat"`
}

// Chat is bin/chat-watch's latest digest of team chat.
type Chat struct {
	At     string   `json:"at"`
	Digest string   `json:"digest"`
	Asks   []string `json:"asks"`
	Log    string   `json:"log"`
}

type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	UpdatedAt string `json:"updatedAt"`
	CreatedAt string `json:"createdAt"`
	ByHand    bool   `json:"by_hand"`
}

type PR struct {
	URL              string `json:"url"`
	Title            string `json:"title"`
	Number           int    `json:"number"`
	ReviewDecision   string `json:"reviewDecision"`
	MergeStateStatus string `json:"mergeStateStatus"`
	UpdatedAt        string `json:"updatedAt"`
	CI               string `json:"ci"`
}

type Review struct {
	Log     string  `json:"log"`
	At      string  `json:"at"`
	URL     *string `json:"url"`
	Verdict string  `json:"verdict"`
	Detail  string  `json:"detail"`
}

type Worker struct {
	Key   string `json:"key"`
	PID   string `json:"pid"`
	Log   string `json:"log"`
	Since string `json:"since"`
}

func (s *Status) SlotsLeft() int { return max(0, s.MaxInFlight-len(s.Owned)) }

func (s *Status) Queued() int {
	n := 0
	for _, q := range s.Queue {
		if !q.ByHand {
			n++
		}
	}
	return n
}

func (s *Status) CountVerdict(v string) int {
	n := 0
	for _, r := range s.Reviews {
		if r.Verdict == v {
			n++
		}
	}
	return n
}

// Stale: the snapshot is older than two ticks, so the scheduler is probably not running.
func (s *Status) Stale() bool { return time.Since(parseAny(s.At)) > 11*time.Minute }

func readStatus(path string) (*Status, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Status
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// parseAny reads the times the hub sees: its own UTC format, GitHub's RFC 3339, and the fleet's local
// "2006-01-02T15:04:05".
func parseAny(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, f := range []string{store.TimeFormat, time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04:05", s, time.Local); err == nil {
		return t
	}
	return time.Time{}
}

func ago(v any) string {
	var t time.Time
	switch x := v.(type) {
	case string:
		t = parseAny(x)
	case time.Time:
		t = x
	}
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now"
	case d < 90*time.Second:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < 90*time.Minute:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func clock(v string) string {
	t := parseAny(v)
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	if time.Since(t) < 20*time.Hour {
		return t.Format("15:04:05")
	}
	return t.Format("Jan 2 15:04")
}

func duration(from, to string) string {
	a, b := parseAny(from), parseAny(to)
	if a.IsZero() {
		return ""
	}
	if b.IsZero() {
		b = time.Now()
	}
	d := b.Sub(a).Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return d.String()
}

func trunc(n int, s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// snippet is the part of s around the first match of q.
func snippet(q, s string) string {
	ls, lq := strings.ToLower(s), strings.ToLower(q)
	i := strings.Index(ls, lq)
	if i < 0 {
		return trunc(240, s)
	}
	start := max(0, i-100)
	end := min(len(s), i+len(q)+140)
	for start > 0 && start < len(s) && (s[start]&0xC0) == 0x80 {
		start--
	}
	for end < len(s) && (s[end]&0xC0) == 0x80 {
		end++
	}
	out := strings.Join(strings.Fields(s[start:end]), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(s) {
		out += "…"
	}
	return out
}

// People maps GitHub logins to display names (hub serve --people "login=Name,..."); unknown logins show as-is.
var People = map[string]string{}

func who(login string) string {
	if n, ok := People[login]; ok && n != "" {
		return n
	}
	return login
}

func statusClass(s string) string {
	switch strings.ToLower(s) {
	case "running", "passing", "approve", "succeeded", "live":
		return "ok"
	case "failed", "failing", "error":
		return "bad"
	case "exited", "ended":
		return "mut"
	}
	return "warn"
}

// state maps any status the hub stores (runs, tasks, terminals, commands, PR checks) onto the one language
// every page uses: working, waiting (for you), blocked, done, failed, idle.
func state(s string) string {
	switch strings.ToLower(s) {
	case "running", "live", "claimed", "starting", "working", "pending", "in_progress", "queued":
		return "working"
	case "waiting", "review", "review_required", "changes_requested":
		return "waiting"
	case "blocked", "paused", "agent:blocked":
		return "blocked"
	case "done", "succeeded", "success", "passing", "approve", "approved", "merged", "ended", "exited", "idle", "closed":
		return "done"
	case "failed", "failing", "error", "cancelled", "stopped":
		return "failed"
	}
	return "idle"
}

var stateWords = map[string]string{"working": "working", "waiting": "waiting for you", "blocked": "blocked", "done": "done",
	"failed": "failed", "idle": "idle", "review": "needs review"}

// stateWord is a status in plain words: the raw status when it says more ("queued", "stopped"), else the state.
func stateWord(s string) string {
	switch strings.ToLower(s) {
	case "queued", "claimed":
		return "queued"
	case "stopped", "cancelled", "ended", "starting":
		return strings.ToLower(s)
	case "live":
		return "live"
	}
	if w, ok := stateWords[s]; ok {
		return w
	}
	return stateWords[state(s)]
}

// kindIcon is a small line icon for a kind of running thing.
func kindIcon(kind string) template.HTML {
	paths := map[string]string{
		"terminal": `<path d="M4 6l5 5-5 5M11 18h9"/>`,
		"task":     `<path d="M9 11l3 3 8-8"/><path d="M20 12v6a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h9"/>`,
		"run":      `<circle cx="12" cy="12" r="8"/><path d="M12 8v4l3 2"/>`,
		"session":  `<path d="M21 12a8 8 0 0 1-11.6 7.1L4 20l1-4.6A8 8 0 1 1 21 12z"/>`,
		"update":   `<path d="M20 12a8 8 0 1 1-2.3-5.6M20 4v5h-5"/>`,
	}
	p, ok := paths[kind]
	if !ok {
		p = `<circle cx="12" cy="12" r="3"/>`
	}
	return template.HTML(`<svg viewBox="0 0 24 24">` + p + `</svg>`)
}

func short(s string) string {
	s = strings.TrimPrefix(s, "https://github.com/")
	return s
}

func homeShort(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		if strings.HasPrefix(p, h) {
			return "~" + p[len(h):]
		}
	}
	for _, pre := range []string{"/Users/", "/home/"} {
		if strings.HasPrefix(p, pre) {
			rest := p[len(pre):]
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				return "~" + rest[i:]
			}
			return "~"
		}
	}
	return p
}

func bytesH(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

var funcs = template.FuncMap{
	"quotaWindow": func(minutes int64) string {
		switch {
		case minutes <= 0:
			return "Unspecified"
		case minutes%1440 == 0:
			return fmt.Sprintf("%d day", minutes/1440)
		case minutes%60 == 0:
			return fmt.Sprintf("%d hour", minutes/60)
		default:
			return fmt.Sprintf("%d minute", minutes)
		}
	},
	"tokens": func(n int64) string { // 44599 -> "44.6k"
		switch {
		case n >= 1_000_000:
			return fmt.Sprintf("%.1fM", float64(n)/1e6)
		case n >= 1000:
			return fmt.Sprintf("%.1fk", float64(n)/1e3)
		}
		return fmt.Sprint(n)
	},
	"remaining": func(used float64) string { return fmt.Sprintf("%.0f", max(0, min(100, 100-used))) },
	"resetTime": func(unix int64) string {
		if unix <= 0 {
			return "Not reported"
		}
		return time.Unix(unix, 0).UTC().Format("Jan 2, 15:04 UTC")
	},
	"expired": func(unix int64) bool { return unix > 0 && time.Now().Unix() >= unix },
	"ago":     ago, "clock": clock, "duration": duration, "trunc": trunc, "snippet": snippet, "who": who,
	"kindLabel": kindLabel, "imageName": func(n string) bool {
		t := mime.TypeByExtension(filepath.Ext(n))
		return strings.HasPrefix(t, "image/") && !strings.Contains(t, "svg")
	}, "viewable": func(name string) bool { return inlineSafe(mime.TypeByExtension(filepath.Ext(name))) }, "statusClass": statusClass, "state": state, "stateWord": stateWord, "kindIcon": kindIcon, "navIn": func(nav string, set ...string) bool {
		for _, x := range set {
			if nav == x {
				return true
			}
		}
		return false
	}, "statusWord": stateWord,
	"closable": closable, "closeArgs": mkClose, "closing": closing, "canClose": func() bool { return false },
	"vnc": vncURL, "stuckOf": mkStuck,
	"final": store.FinalStatus, "short": short, "home": homeShort, "bytes": bytesH,
	"lines": func(s string) int { return strings.Count(s, "\n") + 1 },
	"agentLabels": func(i Issue) []string {
		var out []string
		for _, l := range i.Labels {
			if strings.HasPrefix(l.Name, "agent") {
				out = append(out, l.Name)
			}
		}
		return out
	},
	"labelClass": func(n string) string {
		switch {
		case strings.Contains(n, "blocked"):
			return "bad"
		case strings.Contains(n, "pr-open"):
			return "ok"
		}
		return "warn"
	},
	"deref": func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	},
	"inc":    func(i int64) int64 { return i + 1 },
	"dec":    func(i int64) int64 { return i - 1 },
	"isLong": func(s string) bool { return len(s) > 1200 || strings.Count(s, "\n") > 24 },
}

// kindLabel names a chat row's kind the way the "+ New" page does.
func kindLabel(k string) string {
	switch k {
	case "terminal":
		return "Terminal"
	case "agent":
		return "Task"
	case "plain":
		return "Model chat"
	}
	return k
}
