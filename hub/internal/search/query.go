package search

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"
)

// Query syntax (all clauses must match):
//
//	deploy rollback        words
//	"kubectl get pods"     an exact phrase
//	deploy*                a prefix
//	-flaky                 must not match
//	uiv3#835 blue-stack-api a/b.go 3f2a…-uuid snake_case   code tokens, matched whole or as a run of parts
//	host:studio type:run provider:codex kind:tool_use tool:bash role:user status:failed
//	session:<id or uuid>  after:2026-10-01 before:2026-10-14
//
// Values of the same filter are ORed (type:run type:event); filter values may be quoted.

type ClauseKind int

const (
	Word ClauseKind = iota
	Phrase
	Prefix
)

type Clause struct {
	Kind ClauseKind
	Text string
}

type Parsed struct {
	Must    []Clause
	Not     []Clause
	Filters map[string][]string // field → values, lowercased (session uuids, hosts as given)
	After   time.Time           // inclusive
	Before  time.Time           // exclusive
}

// Fields that are filters. Anything else with a colon (http://x, C:\x, a:b) is an ordinary word.
var filterFields = map[string]string{
	"host": "host", "type": "type", "provider": "provider", "kind": "kind", "tool": "tool", "role": "role",
	"status": "status", "session": "session", "after": "after", "before": "before",
	"since": "after", "until": "before", "is": "type", "in": "type",
}

var typeAlias = map[string]string{
	"message": "message", "messages": "message", "msg": "message", "msgs": "message",
	"session": "session", "sessions": "session", "conversation": "session", "conversations": "session",
	"run": "run", "runs": "run", "event": "event", "events": "event", "log": "event",
}

func (p *Parsed) Empty() bool {
	return len(p.Must) == 0 && len(p.Not) == 0 && len(p.Filters) == 0 && p.After.IsZero() && p.Before.IsZero()
}

// HasText reports whether the query has words to rank by.
func (p *Parsed) HasText() bool { return len(p.Must) > 0 }

func (p *Parsed) addFilter(field, v string) {
	if p.Filters == nil {
		p.Filters = map[string][]string{}
	}
	p.Filters[field] = append(p.Filters[field], v)
}

// Parse reads the query syntax. It never fails: whatever it can't read as syntax is a word.
func Parse(s string, loc *time.Location) *Parsed {
	if loc == nil {
		loc = time.Local
	}
	p := &Parsed{}
	rs := []rune(s)
	i := 0
	for i < len(rs) {
		if unicode.IsSpace(rs[i]) {
			i++
			continue
		}
		neg := false
		if rs[i] == '-' && i+1 < len(rs) && !unicode.IsSpace(rs[i+1]) && rs[i+1] != '-' {
			neg = true
			i++
		}
		if rs[i] == '"' {
			j := i + 1
			for j < len(rs) && rs[j] != '"' {
				j++
			}
			text := strings.TrimSpace(string(rs[i+1 : j]))
			i = j + 1
			if text != "" {
				p.add(neg, Clause{Phrase, text})
			}
			continue
		}
		j := i
		for j < len(rs) && !unicode.IsSpace(rs[j]) {
			if rs[j] == ':' && j+1 < len(rs) && rs[j+1] == '"' {
				break
			}
			j++
		}
		tok := string(rs[i:j])
		// field:"quoted value"
		if j < len(rs) && rs[j] == ':' {
			k := j + 2
			for k < len(rs) && rs[k] != '"' {
				k++
			}
			if f, ok := filterFields[strings.ToLower(tok)]; ok && !neg {
				p.filter(f, string(rs[j+2:min(k, len(rs))]), loc)
			} else {
				p.add(neg, Clause{Phrase, string(rs[j+2 : min(k, len(rs))])})
			}
			i = k + 1
			continue
		}
		i = j
		if c := strings.IndexByte(tok, ':'); c > 0 && c < len(tok)-1 && !neg {
			if f, ok := filterFields[strings.ToLower(tok[:c])]; ok {
				p.filter(f, tok[c+1:], loc)
				continue
			}
		}
		if strings.HasSuffix(tok, "*") {
			stem := strings.TrimRight(tok, "*")
			if a := AnalyzeWord(stem); len(a.Parts) > 0 && len(stem) >= 2 {
				p.add(neg, Clause{Prefix, stem})
				continue
			}
			tok = stem
		}
		if len(AnalyzeWord(tok).Parts) > 0 {
			p.add(neg, Clause{Word, tok})
		}
	}
	return p
}

func (p *Parsed) add(neg bool, c Clause) {
	if neg {
		p.Not = append(p.Not, c)
	} else {
		p.Must = append(p.Must, c)
	}
}

func (p *Parsed) filter(field, v string, loc *time.Location) {
	v = strings.TrimSpace(v)
	if v == "" {
		return
	}
	switch field {
	case "after", "before":
		t, ok := parseDate(v, loc)
		if !ok {
			p.add(false, Clause{Word, field + ":" + v})
			return
		}
		if field == "after" {
			p.After = t
		} else {
			p.Before = t
		}
	case "type":
		if t, ok := typeAlias[strings.ToLower(v)]; ok {
			p.addFilter("type", t)
		} else {
			p.addFilter("type", strings.ToLower(v))
		}
	case "host", "session":
		p.addFilter(field, v)
	default:
		p.addFilter(field, strings.ToLower(v))
	}
}

// parseDate reads 2026-10-14, 2026-10-14T15:04, RFC 3339, "today", "yesterday", or a relative 7d / 12h.
func parseDate(v string, loc *time.Location) (time.Time, bool) {
	now := time.Now().In(loc)
	switch strings.ToLower(v) {
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, loc), true
	case "yesterday":
		y, m, d := now.AddDate(0, 0, -1).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, loc), true
	}
	if n := len(v); n >= 2 {
		if k, err := strconv.Atoi(v[:n-1]); err == nil && k >= 0 {
			switch v[n-1] {
			case 'd':
				return now.AddDate(0, 0, -k), true
			case 'h':
				return now.Add(-time.Duration(k) * time.Hour), true
			case 'w':
				return now.AddDate(0, 0, -7*k), true
			}
		}
	}
	for _, f := range []string{"2006-01-02", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01"} {
		if t, err := time.ParseInLocation(f, v, loc); err == nil {
			return t, true
		}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// Text fields every word is looked for in; a title match counts double.
var textFields = []struct {
	name  string
	boost float64
}{{"body", 1}, {"title", 2}}

func boosted(q query.Query, b float64) query.Query {
	if bq, ok := q.(query.BoostableQuery); ok && b != 1 {
		bq.SetBoost(b)
	}
	return q
}

func clauseQuery(c Clause) query.Query {
	var alts []query.Query
	for _, f := range textFields {
		switch c.Kind {
		case Word:
			a := AnalyzeWord(c.Text)
			if len(a.Parts) == 1 {
				alts = append(alts, boosted(termQ(a.Parts[0], f.name), f.boost))
				continue
			}
			if a.Whole != "" {
				alts = append(alts, boosted(termQ(a.Whole, f.name), f.boost*1.5))
			}
			alts = append(alts, boosted(query.NewPhraseQuery(a.Parts, f.name), f.boost))
		case Prefix:
			a := AnalyzeWord(c.Text)
			stem := a.Whole
			if stem == "" {
				if len(a.Parts) > 1 {
					// "deploy ap*" can't be: a prefix word is one token; use its last part after a phrase of the rest.
					conj := query.NewConjunctionQuery([]query.Query{query.NewPhraseQuery(a.Parts[:len(a.Parts)-1], f.name), prefixQ(a.Parts[len(a.Parts)-1], f.name)})
					alts = append(alts, boosted(conj, f.boost))
					continue
				}
				stem = a.Parts[0]
			}
			alts = append(alts, boosted(prefixQ(stem, f.name), f.boost))
		case Phrase:
			terms := AnalyzePhrase(c.Text)
			switch {
			case len(terms) == 0:
				continue
			case len(terms) == 1 && len(terms[0]) == 1:
				alts = append(alts, boosted(termQ(terms[0][0], f.name), f.boost))
			default:
				alts = append(alts, boosted(query.NewMultiPhraseQuery(terms, f.name), f.boost))
			}
		}
	}
	if len(alts) == 0 {
		return nil
	}
	if len(alts) == 1 {
		return alts[0]
	}
	return query.NewDisjunctionQuery(alts)
}

func termQ(t, field string) query.Query {
	q := query.NewTermQuery(t)
	q.SetField(field)
	return q
}

func prefixQ(t, field string) query.Query {
	q := query.NewPrefixQuery(t)
	q.SetField(field)
	return q
}

// Build turns a parsed query into a bleve query. Filters don't affect the score.
func (p *Parsed) Build() query.Query {
	bq := query.NewBooleanQuery(nil, nil, nil)
	var must []query.Query
	for _, c := range p.Must {
		if q := clauseQuery(c); q != nil {
			must = append(must, q)
		}
	}
	if len(must) == 0 {
		must = append(must, query.NewMatchAllQuery())
	}
	bq.AddMust(must...)
	for _, c := range p.Not {
		if q := clauseQuery(c); q != nil {
			bq.AddMustNot(q)
		}
	}
	var filters []query.Query
	for field, vals := range p.Filters {
		var alts []query.Query
		for _, v := range vals {
			switch field {
			case "session":
				if _, err := strconv.ParseInt(v, 10, 64); err == nil {
					alts = append(alts, termQ(v, "session"))
				} else {
					alts = append(alts, prefixQ(strings.ToLower(v), "suuid"))
				}
			case "host":
				alts = append(alts, termQ(strings.ToLower(v), "host"))
			case "tool":
				alts = append(alts, termQ(v, "tool"))
			default:
				alts = append(alts, termQ(v, field))
			}
		}
		if len(alts) == 1 {
			filters = append(filters, alts[0])
		} else {
			filters = append(filters, query.NewDisjunctionQuery(alts))
		}
	}
	if !p.After.IsZero() || !p.Before.IsZero() {
		incl, excl := true, false
		dq := bleve.NewDateRangeInclusiveQuery(p.After, p.Before, &incl, &excl)
		dq.SetField("time")
		filters = append(filters, dq)
	}
	if len(filters) == 1 {
		bq.AddFilter(filters[0])
	} else if len(filters) > 1 {
		bq.AddFilter(query.NewConjunctionQuery(filters))
	}
	return bq
}

// HighlightTerms is what the snippet marks: phrases as written, then each word's parts and wholes.
func (p *Parsed) HighlightTerms() []HTerm {
	var out []HTerm
	seen := map[string]bool{}
	add := func(t string, prefix bool) {
		t = strings.ToLower(t)
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, HTerm{Text: t, Prefix: prefix})
	}
	for _, c := range p.Must {
		switch c.Kind {
		case Phrase:
			add(c.Text, false)
			for _, pos := range AnalyzePhrase(c.Text) {
				for _, t := range pos {
					add(t, false)
				}
			}
		case Word, Prefix:
			a := AnalyzeWord(c.Text)
			add(a.Whole, c.Kind == Prefix)
			for i, t := range a.Parts {
				add(t, c.Kind == Prefix && i == len(a.Parts)-1)
			}
		}
	}
	return out
}
