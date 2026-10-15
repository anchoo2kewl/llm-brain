package memory

import (
	"context"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The context pack: what an agent should know before it works on a repo, issue, PR, folder and files.

// Request is what a pack is for.
type Request struct {
	Repo   string   `json:"repo,omitempty"`
	Issue  int      `json:"issue,omitempty"`
	PR     int      `json:"pr,omitempty"`
	Folder string   `json:"folder,omitempty"`
	Files  []string `json:"files,omitempty"`
	Query  string   `json:"query,omitempty"` // extra words: the task, an error message
	K      int      `json:"k,omitempty"`     // how many memories (default 8, at most 50)
	Self   string   `json:"self,omitempty"`  // the asker's session id, left out of presence
}

// Item is a memory in a pack with why it was picked.
type Item struct {
	Memory
	Score  float64 `json:"score"`
	Why    string  `json:"why"`
	Verify bool    `json:"verify"` // it names a file, flag or function: check it still exists before relying on it
}

// Pack is the answer.
type Pack struct {
	Memories []Item     `json:"memories"`
	Wiki     []Hit      `json:"wiki,omitempty"` // TaskAI hits that aren't hub memories (pages people wrote)
	Presence []Presence `json:"presence"`
	Remote   string     `json:"remote"` // ok | cached | timeout | error | off
	TookMS   float64    `json:"took_ms"`
}

// Context builds the pack: memories scored on the hub, TaskAI's search merged in when it answers within
// the budget (else from the cache, else without), and who else is on it.
func (s *Service) Context(ctx context.Context, src Source, r Request) (Pack, error) {
	start := time.Now()
	if r.K <= 0 {
		r.K = 8
	}
	r.K = min(r.K, 50)
	r.Repo = strings.TrimSuffix(strings.TrimSpace(r.Repo), ".git")
	ms, err := s.Active(ctx)
	if err != nil {
		return Pack{}, err
	}
	now := s.clock()
	items := Score(ms, r, now)
	pk := Pack{Remote: "off"}
	if s.TaskAI != nil {
		var hits []Hit
		hits, pk.Remote = s.remote(ctx, r)
		items, pk.Wiki = merge(items, hits, ms, now)
	}
	if len(items) > r.K {
		items = items[:r.K]
	}
	if len(pk.Wiki) > 3 {
		pk.Wiki = pk.Wiki[:3]
	}
	pk.Memories = items
	if pk.Memories == nil {
		pk.Memories = []Item{}
	}
	if want := (Want{Repo: r.Repo, Issue: r.Issue, PR: r.PR, Folder: r.Folder, Self: r.Self}); src != nil && !want.empty() {
		if pk.Presence, err = Who(ctx, src, want); err != nil {
			return Pack{}, err
		}
	}
	if pk.Presence == nil {
		pk.Presence = []Presence{}
	}
	pk.TookMS = float64(time.Since(start).Microseconds()) / 1000
	return pk, nil
}

// Score ranks memories for r: scope first (issue > PR > files > folder > repo), then kind, confidence,
// words in common with the query, and age (half-life 60 days). Memories outside r's scope are left out.
func Score(ms []Memory, r Request, now time.Time) []Item {
	qwords := wordSet(r.Query + " " + strings.Join(fileWords(r.Files), " "))
	var out []Item
	for _, m := range ms {
		if !m.Active(now) {
			continue
		}
		sc, why := scopeScore(m.Scope, r)
		if sc <= 0 {
			continue
		}
		if m.Kind == KindHandoff && !(r.Issue > 0 && m.Scope.Issue == r.Issue) && !(r.PR > 0 && m.Scope.PR == r.PR) {
			continue // a handoff is for whoever picks up that issue or PR next
		}
		sc += map[string]float64{KindGotcha: 1, KindDecision: 0.8, KindHandoff: 1.5, KindRunbook: 0.5}[m.Kind]
		sc *= map[string]float64{"low": 0.7, "medium": 1, "high": 1.2}[m.Confidence]
		if len(qwords) > 0 {
			n := 0
			for w := range wordSet(m.Title + " " + m.Body) {
				if qwords[w] {
					n++
				}
			}
			sc += math.Min(float64(n)*0.4, 2)
		}
		age := now.Sub(m.CreatedAt).Hours() / 24
		sc *= 0.5 + 0.5*math.Pow(0.5, age/60)
		out = append(out, Item{Memory: m, Score: math.Round(sc*100) / 100, Why: why, Verify: namesCode(m.Body)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func scopeScore(m Scope, r Request) (float64, string) {
	repo := m.Repo != "" && r.Repo != "" && sameRepo(m.Repo, r.Repo)
	folder := m.Folder != "" && r.Folder != "" && underOrSame(m.Folder, r.Folder)
	if !repo && !folder {
		return 0, ""
	}
	if (m.Issue > 0 || m.PR > 0) && !(r.Issue > 0 && m.Issue == r.Issue) && !(r.PR > 0 && m.PR == r.PR) {
		// about another issue or PR in this repo: still useful, but less
		if hit := fileHits(m.Files, r.Files); hit > 0 {
			return 2 + float64(hit), "same files"
		}
		return 0.6, "same repo, other issue"
	}
	switch {
	case r.Issue > 0 && m.Issue == r.Issue:
		return 6, "this issue"
	case r.PR > 0 && m.PR == r.PR:
		return 6, "this PR"
	}
	if hit := fileHits(m.Files, r.Files); hit > 0 {
		return 3 + float64(min(hit, 3)), "same files"
	}
	if folder {
		return 2.5, "this folder"
	}
	return 2, "this repo"
}

// fileHits counts files in common, by path or by a directory one names.
func fileHits(mf, rf []string) int {
	n := 0
	for _, a := range mf {
		for _, b := range rf {
			if a == b || strings.HasPrefix(b, strings.TrimRight(a, "/")+"/") {
				n++
				break
			}
		}
	}
	return n
}

func fileWords(fs []string) []string {
	var out []string
	for _, f := range fs {
		out = append(out, strings.TrimSuffix(path.Base(f), path.Ext(f)))
	}
	return out
}

// codeRe: `backticked` names, paths with an extension, --flags, and calls like Foo().
var codeRe = regexp.MustCompile("`[^`\\n]+`|\\b[\\w.-]+/[\\w./-]+\\.\\w{1,5}\\b|(^|\\s)--[a-z][\\w-]+|\\b\\w+\\(\\)")

func namesCode(body string) bool { return codeRe.MatchString(body) }

// merge boosts hub memories TaskAI also found (it matched them by meaning, not only by scope) and keeps
// TaskAI hits that are not hub memories (pages people wrote by hand) apart.
func merge(items []Item, hits []Hit, all []Memory, now time.Time) ([]Item, []Hit) {
	if len(hits) == 0 {
		return items, nil
	}
	byPage := map[int64]Memory{}
	for _, m := range all {
		if m.PageID > 0 {
			byPage[m.PageID] = m
		}
	}
	at := map[int64]int{}
	for i, it := range items {
		at[it.ID] = i
	}
	var wiki []Hit
	seen := map[int64]bool{}
	for rank, h := range hits {
		m, ours := byPage[h.PageID]
		if !ours {
			if !seen[h.PageID] && !strings.HasPrefix(h.PageTitle, "[superseded]") {
				seen[h.PageID] = true
				wiki = append(wiki, h)
			}
			continue
		}
		if i, ok := at[m.ID]; ok {
			items[i].Score += 1.5 / float64(rank+1)
			items[i].Why += ", search"
		}
		// A memory TaskAI found that the scope didn't pick (another repo) stays out: scope is the filter.
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Score > items[j].Score })
	return items, wiki
}

// ---------------------------------------------------------------- TaskAI search, within a budget

type remoteCache struct {
	mu sync.Mutex
	m  map[string]cached
}

type cached struct {
	hits []Hit
	at   time.Time
	err  bool
}

const cacheTTL = 2 * time.Minute

func (c *remoteCache) get(k string) (cached, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	if ok && time.Since(v.at) > cacheTTL {
		delete(c.m, k)
		return cached{}, false
	}
	return v, ok
}

func (c *remoteCache) put(k string, v cached) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cached{}
	}
	if len(c.m) > 500 {
		c.m = map[string]cached{}
	}
	c.m[k] = v
}

func (c *remoteCache) clear() {
	c.mu.Lock()
	c.m = nil
	c.mu.Unlock()
}

// searchText is the query sent to TaskAI: the repo and issue as they appear on memory pages, the file names
// and the extra words.
func searchText(r Request) string {
	var p []string
	if r.Repo != "" {
		p = append(p, r.Repo)
	}
	if r.Issue > 0 {
		p = append(p, fmt.Sprintf("#%d", r.Issue))
	}
	if r.PR > 0 {
		p = append(p, fmt.Sprintf("PR %d", r.PR))
	}
	p = append(p, fileWords(r.Files)...)
	if q := strings.TrimSpace(r.Query); q != "" {
		p = append(p, q)
	}
	if len(p) == 0 && r.Folder != "" {
		p = append(p, path.Base(r.Folder))
	}
	t := strings.Join(p, " ")
	if len(t) > 500 {
		t = t[:500]
	}
	return t
}

// remote answers from the cache, else asks TaskAI and waits up to the budget. A search that outlives the
// budget still finishes in the background and fills the cache for the next pack.
func (s *Service) remote(ctx context.Context, r Request) ([]Hit, string) {
	q := searchText(r)
	if q == "" {
		return nil, "off"
	}
	if v, ok := s.cache.get(q); ok {
		if v.err {
			return nil, "error"
		}
		return v.hits, "cached"
	}
	budget := s.RemoteBudget
	if budget <= 0 {
		budget = 150 * time.Millisecond
	}
	mode := s.RemoteMode
	if mode == "" {
		mode = "hybrid"
	}
	done := make(chan cached, 1)
	go func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		hits, err := s.TaskAI.Search(sctx, q, mode, 20, 0)
		v := cached{hits: hits, at: time.Now(), err: err != nil}
		if err != nil {
			s.logf("memory: TaskAI search: %v", err)
		}
		s.cache.put(q, v)
		done <- v
	}()
	select {
	case v := <-done:
		if v.err {
			return nil, "error"
		}
		return v.hits, "ok"
	case <-time.After(budget):
		return nil, "timeout"
	case <-ctx.Done():
		return nil, "timeout"
	}
}

// ---------------------------------------------------------------- markdown, for prompts and AGENTS.md

// Markdown renders a pack for a prompt: short, with a note to verify anything that names code.
func Markdown(p Pack, r Request) string {
	if len(p.Memories) == 0 && len(p.Presence) == 0 && len(p.Wiki) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Shared memory (llm-brain)\n\n")
	b.WriteString("What other agents learned here. Treat it as notes from a colleague: verify anything that names a file, flag or function before relying on it.\n\n")
	for _, it := range p.Memories {
		fmt.Fprintf(&b, "- **%s** (%s, %s): %s", it.Kind, ScopeLabel(it.Scope), it.CreatedAt.Format("2006-01-02"), it.Title)
		body := strings.TrimSpace(it.Body)
		if body != "" && body != it.Title {
			fmt.Fprintf(&b, "\n  %s", strings.ReplaceAll(body, "\n", "\n  "))
		}
		if it.Verify {
			b.WriteString("\n  _(verify before use)_")
		}
		b.WriteString("\n")
	}
	for _, h := range p.Wiki {
		fmt.Fprintf(&b, "- wiki: **%s**: %s\n", h.PageTitle, strings.ReplaceAll(strings.TrimSpace(h.Snippet), "\n", " "))
	}
	if len(p.Presence) > 0 {
		b.WriteString("\n### Also working on this now\n\n")
		for i, x := range p.Presence {
			if i == 8 {
				fmt.Fprintf(&b, "- … and %d more\n", len(p.Presence)-i)
				break
			}
			what := x.Title
			if what == "" {
				what = x.Folder
			}
			fmt.Fprintf(&b, "- %s on %s (%s", nonEmpty(x.Provider, x.Kind), x.Host, x.Why)
			if x.Folder != "" {
				fmt.Fprintf(&b, ", `%s`", x.Folder)
			}
			fmt.Fprintf(&b, "): %s\n", what)
		}
		b.WriteString("\nCoordinate before changing the same files; leave a `handoff` memory when you stop.\n")
	}
	return b.String()
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
