package search

import (
	"context"
	"fmt"
	"html/template"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

type Request struct {
	Q      string
	Host   string // from the host menu; same as host: in the query
	Type   string // from the type menu; same as type: in the query
	Newest bool   // sort by time instead of relevance
	Page   int    // 1-based
	Size   int
}

// Hit is one result, ready to show.
type Hit struct {
	Type     string // message | session | run | event
	ID       int64
	Host     string
	Provider string
	Time     string // store time format
	Title    string
	Label    string // what it is: "assistant · Bash", "review · succeeded", …
	Link     string
	Context  string // a second link: a message with the transcript before it
	Snippet  template.HTML
}

type Result struct {
	Total uint64
	Hits  []Hit
	Took  time.Duration
}

// Search runs a query against the index and turns the page of hits into results from the store.
func (x *Index) Search(ctx context.Context, r Request) (*Result, error) {
	start := time.Now()
	p := Parse(r.Q, x.Loc)
	if r.Host != "" {
		p.addFilter("host", r.Host)
	}
	if r.Type != "" {
		p.filter("type", r.Type, x.Loc)
	}
	if p.Empty() {
		return &Result{}, nil
	}
	if r.Size <= 0 {
		r.Size = 30
	}
	if r.Page < 1 {
		r.Page = 1
	}
	req := bleve.NewSearchRequestOptions(p.Build(), r.Size, (r.Page-1)*r.Size, false)
	if r.Newest || !p.HasText() {
		req.SortBy([]string{"-time"})
		req.Score = "none" // nothing reads the score: don't compute it
	}
	sr, err := x.bx.SearchInContext(ctx, req)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(sr.Hits))
	for i, h := range sr.Hits {
		ids[i] = h.ID
	}
	hits, stale, err := Hydrate(ctx, x.st, ids, p.HighlightTerms())
	if err != nil {
		return nil, err
	}
	if len(stale) > 0 {
		// Rows that are gone (a transcript that was rewritten from the start): drop them from the index.
		go func() {
			b := x.bx.NewBatch()
			for _, id := range stale {
				b.Delete(id)
			}
			_ = x.bx.Batch(b)
		}()
	}
	return &Result{Total: sr.Total, Hits: hits, Took: time.Since(start)}, nil
}

// Hydrate loads the rows behind index document ids, in the given order, and builds their results. Ids
// whose rows no longer exist come back as stale.
func Hydrate(ctx context.Context, st *store.Store, ids []string, terms []HTerm) ([]Hit, []string, error) {
	var mids, sids, rids, eids []int64
	for _, id := range ids {
		t, n, ok := parseDocID(id)
		if !ok {
			continue
		}
		switch t {
		case "m":
			mids = append(mids, n)
		case "s":
			sids = append(sids, n)
		case "r":
			rids = append(rids, n)
		case "e":
			eids = append(eids, n)
		}
	}
	byID := map[string]Hit{}
	ms, err := st.MessagesByID(ctx, mids)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range ms {
		byID[docID("m", m.ID)] = messageHit(m, terms)
	}
	ss, err := st.SessionsByID(ctx, sids)
	if err != nil {
		return nil, nil, err
	}
	for _, s := range ss {
		byID[docID("s", s.ID)] = sessionHit(s, terms)
	}
	rs, err := st.RunsByID(ctx, rids)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rs {
		byID[docID("r", r.ID)] = runHit(r, terms)
	}
	es, err := st.EventsByID(ctx, eids)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range es {
		byID[docID("e", e.ID)] = eventHit(e, terms)
	}
	out := make([]Hit, 0, len(ids))
	var stale []string
	for _, id := range ids {
		if h, ok := byID[id]; ok {
			out = append(out, h)
		} else {
			stale = append(stale, id)
		}
	}
	return out, stale, nil
}

func messageHit(m store.Hit, terms []HTerm) Hit {
	label := m.Role + " · " + m.Kind
	if m.ToolName != "" {
		label = m.Role + " · " + m.ToolName
	}
	text := m.Text
	if m.ToolInput != "" {
		text = m.ToolInput + "\n" + m.Text
	}
	return Hit{
		Type: "message", ID: m.ID, Host: m.Session.Host, Provider: m.Session.Provider, Time: m.TS,
		Title: m.Session.DisplayTitle(), Label: label,
		Link:    fmt.Sprintf("/sessions/%d?after=%d#m%d", m.SessionID, m.ID-1, m.ID),
		Context: fmt.Sprintf("/sessions/%d?before=%d#m%d", m.SessionID, m.ID+1, m.ID),
		Snippet: Snippet(text, terms),
	}
}

func sessionHit(s store.Session, terms []HTerm) Hit {
	label := s.Provider + " session"
	if s.ParentUUID != "" {
		label = s.Provider + " subagent"
	}
	if s.Branch != "" {
		label += " · " + s.Branch
	}
	return Hit{
		Type: "session", ID: s.ID, Host: s.Host, Provider: s.Provider, Time: s.LastAt, Title: s.DisplayTitle(), Label: label,
		Link:    fmt.Sprintf("/sessions/%d", s.ID),
		Snippet: Snippet(joinNonEmpty(s.Title, s.AutoTitle, s.AgentName, s.Cwd, s.UUID), terms),
	}
}

func runHit(r store.Run, terms []HTerm) Hit {
	return Hit{
		Type: "run", ID: r.ID, Host: r.Host, Provider: r.Provider, Time: r.StartedAt, Title: r.Key,
		Label:   joinWith(" · ", r.Kind, r.Status, r.Target),
		Link:    fmt.Sprintf("/runs/%d", r.ID),
		Snippet: Snippet(joinNonEmpty(r.Summary, r.Target, r.Prompt, r.Log), terms),
	}
}

var (
	logRef = regexp.MustCompile(`log (\S+\.log)\b`)
	keyRef = regexp.MustCompile(`\b((?:issue|pr|review)-[A-Za-z0-9._-]+?)(?:\.\d{8}-\d{6})?\b`)
)

func eventHit(e store.IndexEvent, terms []HTerm) Hit {
	h := Hit{Type: "event", ID: e.ID, Host: e.Host, Time: e.TS, Title: eventTitle(e.Path), Label: e.Level, Snippet: Snippet(e.Text, terms)}
	// An event that names a worker log links to its run; one that names a worker key, to that key's runs.
	if m := logRef.FindStringSubmatch(e.Text); m != nil {
		h.Link = "/log/" + url.PathEscape(filepath.Base(m[1])) + "?host=" + url.QueryEscape(e.Host)
	} else if m := keyRef.FindStringSubmatch(e.Text); m != nil {
		h.Link = "/runs?host=" + url.QueryEscape(e.Host) + "&q=" + url.QueryEscape(m[1])
	} else {
		h.Link = "/search?q=" + url.QueryEscape(fmt.Sprintf("type:event host:%s", e.Host)) + "&sort=new"
	}
	if k := keyRef.FindStringSubmatch(e.Text); k != nil {
		h.Title = "fleet.log · " + k[1]
	}
	return h
}

func joinWith(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// FromStore turns the store's LIKE-scan results (the path used until the index is built) into results.
func FromStore(q string, msgs []store.Hit, runs []store.Run) []Hit {
	terms := append([]HTerm{{Text: strings.ToLower(q)}}, Parse(q, nil).HighlightTerms()...)
	out := make([]Hit, 0, len(msgs)+len(runs))
	for _, r := range runs {
		out = append(out, runHit(r, terms))
	}
	for _, m := range msgs {
		out = append(out, messageHit(m, terms))
	}
	return out
}

// eventTitle names the log an event came from: fleet.log (scheduler) or chat.log (team chat).
func eventTitle(path string) string {
	if b := filepath.Base(path); b == "chat.log" {
		return b
	}
	return "fleet.log"
}
