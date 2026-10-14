// Package search is the hub's full-text index: bleve (scorch, pure Go) beside the Turso store, which has no
// FTS here. Every message, session, run and fleet.log event is a document. The index follows the store by
// watermark: messages and events are append-only, so the index remembers the highest id it has of each
// table (in the same bleve batch as the documents, so the two never disagree); sessions and runs change in
// place, so the ingest transaction that changes one queues it in search_dirty. A crash or restart re-indexes
// only what's past the watermarks, and document ids are the row ids, so nothing is indexed twice.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	index "github.com/blevesearch/bleve_index_api"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// version names the mapping and analyzer. An index built with another version is rebuilt from scratch.
const version = "2"

var (
	keyVersion = []byte("version")
	keyReady   = []byte("ready") // set once the first full build finished
)

func wmKey(table string) []byte { return []byte("wm:" + table) }

// tables in the order a build walks them: the small ones first, so sessions and runs are searchable early.
var tables = []string{"sessions", "runs", "events", "messages"}

type Index struct {
	st  *store.Store
	bx  bleve.Index
	log *log.Logger
	Loc *time.Location // for before:/after: dates

	// PageSize is how many rows one read takes from the store (and one bleve batch indexes).
	PageSize int

	mu      sync.Mutex // one writer at a time; held for a whole catch-up, never across a store call that waits on it
	pending atomic.Bool
	ready   atomic.Bool
	pct     atomic.Int64
	wm      map[string]int64 // guarded by mu
	built   time.Time        // when this process started a build, zero if the index was already built
	closed  bool             // guarded by mu
}

func newMapping() mapping.IndexMapping {
	im := bleve.NewIndexMapping()
	im.ScoringModel = index.BM25Scoring
	im.DefaultAnalyzer = AnalyzerName
	im.StoreDynamic, im.IndexDynamic, im.DocValuesDynamic = false, false, false
	text := func() *mapping.FieldMapping {
		f := bleve.NewTextFieldMapping()
		f.Analyzer = AnalyzerName
		f.Store, f.IncludeInAll, f.DocValues = false, false, false
		f.IncludeTermVectors = true // positions, for phrases
		return f
	}
	kw := func() *mapping.FieldMapping {
		f := bleve.NewKeywordFieldMapping()
		f.Store, f.IncludeInAll, f.DocValues, f.IncludeTermVectors = false, false, false, false
		return f
	}
	dm := bleve.NewDocumentStaticMapping()
	dm.AddFieldMappingsAt("body", text())
	dm.AddFieldMappingsAt("title", text())
	for _, k := range []string{"type", "host", "provider", "kind", "role", "tool", "status", "session", "suuid"} {
		dm.AddFieldMappingsAt(k, kw())
	}
	t := bleve.NewDateTimeFieldMapping()
	t.Store, t.IncludeInAll, t.DocValues = false, false, true // sort by newest
	dm.AddFieldMappingsAt("time", t)
	im.DefaultMapping = dm
	return im
}

// Open opens the index at path, creating it (or rebuilding it, when it is from another version or
// unreadable). An empty path is an in-memory index, for tests.
func Open(path string, st *store.Store, lg *log.Logger) (*Index, error) {
	if lg == nil {
		lg = log.New(os.Stderr, "", log.LstdFlags)
	}
	x := &Index{st: st, log: lg, Loc: time.Local, PageSize: 1000, wm: map[string]int64{}}
	var bx bleve.Index
	var err error
	if path == "" {
		bx, err = bleve.NewMemOnly(newMapping())
		if err != nil {
			return nil, err
		}
		if err := bx.SetInternal(keyVersion, []byte(version)); err != nil {
			return nil, err
		}
	} else {
		bx, err = bleve.Open(path)
		switch {
		case err == nil:
			if v, _ := bx.GetInternal(keyVersion); string(v) != version {
				lg.Printf("search index %s is version %q, want %q: rebuilding", path, v, version)
				_ = bx.Close()
				bx = nil
				if err := removeIndex(path); err != nil {
					return nil, err
				}
			}
		case errors.Is(err, bleve.ErrorIndexPathDoesNotExist):
			bx = nil // Open returns a typed nil on error
		default:
			bx = nil
			lg.Printf("search index %s unreadable (%v): rebuilding", path, err)
			if err := removeIndex(path); err != nil {
				return nil, err
			}
		}
		if bx == nil {
			if bx, err = bleve.New(path, newMapping()); err != nil {
				return nil, fmt.Errorf("create search index %s: %w", path, err)
			}
			if err := bx.SetInternal(keyVersion, []byte(version)); err != nil {
				_ = bx.Close()
				return nil, err
			}
		}
	}
	x.bx = bx
	for _, t := range tables {
		v, err := bx.GetInternal(wmKey(t))
		if err != nil {
			_ = bx.Close()
			return nil, err
		}
		x.wm[t], _ = strconv.ParseInt(string(v), 10, 64)
	}
	if v, _ := bx.GetInternal(keyReady); string(v) == "1" {
		x.ready.Store(true)
		x.pct.Store(100)
	}
	return x, nil
}

// removeIndex deletes an index directory, and only something that looks like one.
func removeIndex(path string) error {
	if _, err := os.Stat(filepath.Join(path, "index_meta.json")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				return nil
			}
		}
		return fmt.Errorf("%s doesn't look like a search index; not deleting it", path)
	}
	return os.RemoveAll(path)
}

// Close waits for a running catch-up to stop (cancel its context first) and closes the index. It is safe to
// call twice.
func (x *Index) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return nil
	}
	x.closed = true
	return x.bx.Close()
}

// Ready reports whether the first full build has finished. Until then, search falls back to the store.
func (x *Index) Ready() bool { return x.ready.Load() }

// Progress is the build's percentage (by messages, which are nearly all of it).
func (x *Index) Progress() int { return int(x.pct.Load()) }

// DocCount is the number of documents in the index.
func (x *Index) DocCount() uint64 {
	n, _ := x.bx.DocCount()
	return n
}

// Watermark is the highest row id of table that the index holds.
func (x *Index) Watermark(table string) int64 {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.wm[table]
}

// Run builds the index if it isn't built, then keeps it current: Sync runs after every ingest write, and
// here every interval as a backstop.
func (x *Index) Run(ctx context.Context, interval time.Duration) {
	if !x.Ready() {
		x.log.Printf("search index: building in the background; search uses the slow path until it's done")
	}
	if err := x.Sync(ctx); err != nil && ctx.Err() == nil {
		x.log.Printf("search index: %v", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := x.Sync(ctx); err != nil && ctx.Err() == nil {
				x.log.Printf("search index: %v", err)
			}
		}
	}
}

// Sync indexes everything the store has that the index doesn't. If another Sync (or the build) is running,
// it returns at once and that one picks the new rows up before it finishes.
func (x *Index) Sync(ctx context.Context) error {
	x.pending.Store(true)
	for {
		if !x.mu.TryLock() {
			return nil
		}
		if x.closed {
			x.mu.Unlock()
			return nil
		}
		var err error
		for err == nil && x.pending.Swap(false) {
			err = x.catchUp(ctx)
		}
		x.mu.Unlock()
		if err != nil {
			x.pending.Store(true) // try again next time
			return err
		}
		if !x.pending.Load() {
			return nil
		}
	}
}

// catchUp runs with mu held.
func (x *Index) catchUp(ctx context.Context) error {
	building := !x.ready.Load()
	if building && x.built.IsZero() {
		x.built = time.Now()
	}
	var maxMsg int64
	if building {
		var err error
		if maxMsg, err = x.st.MaxID(ctx, "messages"); err != nil {
			return err
		}
	}
	for _, t := range tables {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := x.indexPage(ctx, t)
			if err != nil {
				return fmt.Errorf("index %s after %d: %w", t, x.wm[t], err)
			}
			if building && t == "messages" && maxMsg > 0 {
				x.pct.Store(min(99, x.wm["messages"]*100/maxMsg))
			}
			if n < x.PageSize {
				break
			}
		}
	}
	if err := x.indexDirty(ctx); err != nil {
		return err
	}
	if building {
		b := x.bx.NewBatch()
		b.SetInternal(keyReady, []byte("1"))
		if err := x.bx.Batch(b); err != nil {
			return err
		}
		x.ready.Store(true)
		x.pct.Store(100)
		x.log.Printf("search index: built %d documents in %s", x.DocCount(), time.Since(x.built).Round(time.Millisecond))
	}
	return nil
}

// indexPage indexes the next page of table past its watermark and returns how many rows it read.
func (x *Index) indexPage(ctx context.Context, table string) (int, error) {
	after := x.wm[table]
	b := x.bx.NewBatch()
	var last int64
	var n int
	switch table {
	case "messages":
		ms, err := x.st.MessagesAfter(ctx, after, x.PageSize)
		if err != nil {
			return 0, err
		}
		for _, m := range ms {
			if d := messageDoc(m); d != nil {
				if err := b.Index(docID("m", m.ID), d); err != nil {
					return 0, err
				}
			}
			last = m.ID
		}
		n = len(ms)
	case "events":
		es, err := x.st.EventsAfter(ctx, after, x.PageSize)
		if err != nil {
			return 0, err
		}
		for _, e := range es {
			if err := b.Index(docID("e", e.ID), eventDoc(e)); err != nil {
				return 0, err
			}
			last = e.ID
		}
		n = len(es)
	case "sessions":
		ss, err := x.st.SessionsAfter(ctx, after, x.PageSize)
		if err != nil {
			return 0, err
		}
		for _, s := range ss {
			if err := b.Index(docID("s", s.ID), sessionDoc(s)); err != nil {
				return 0, err
			}
			last = s.ID
		}
		n = len(ss)
	case "runs":
		rs, err := x.st.RunsAfter(ctx, after, x.PageSize)
		if err != nil {
			return 0, err
		}
		for _, r := range rs {
			if err := b.Index(docID("r", r.ID), runDoc(r)); err != nil {
				return 0, err
			}
			last = r.ID
		}
		n = len(rs)
	}
	if n == 0 {
		return 0, nil
	}
	b.SetInternal(wmKey(table), []byte(strconv.FormatInt(last, 10)))
	if err := x.bx.Batch(b); err != nil {
		return 0, err
	}
	x.wm[table] = last
	return n, nil
}

// indexDirty re-indexes the sessions and runs that changed since they were last indexed.
func (x *Index) indexDirty(ctx context.Context) error {
	const limit = 500
	for pass := 0; pass < 20; pass++ {
		ds, err := x.st.DirtyRows(ctx, limit)
		if err != nil || len(ds) == 0 {
			return err
		}
		var sids, rids []int64
		for _, d := range ds {
			switch d.Kind {
			case "s":
				sids = append(sids, d.ID)
			case "r":
				rids = append(rids, d.ID)
			}
		}
		b := x.bx.NewBatch()
		ss, err := x.st.SessionsByID(ctx, sids)
		if err != nil {
			return err
		}
		for _, s := range ss {
			if err := b.Index(docID("s", s.ID), sessionDoc(s)); err != nil {
				return err
			}
		}
		rs, err := x.st.RunsByID(ctx, rids)
		if err != nil {
			return err
		}
		for _, r := range rs {
			if err := b.Index(docID("r", r.ID), runDoc(r)); err != nil {
				return err
			}
		}
		if err := x.bx.Batch(b); err != nil {
			return err
		}
		if err := x.st.ClearDirty(ctx, ds); err != nil {
			return err
		}
		if len(ds) < limit {
			return nil
		}
	}
	return nil
}

func docID(prefix string, id int64) string { return prefix + strconv.FormatInt(id, 10) }

func parseDocID(s string) (string, int64, bool) {
	if len(s) < 2 {
		return "", 0, false
	}
	id, err := strconv.ParseInt(s[1:], 10, 64)
	return s[:1], id, err == nil
}

func setTime(d map[string]any, ts ...string) {
	for _, s := range ts {
		if t := store.ParseT(s); !t.IsZero() {
			d["time"] = t
			return
		}
	}
}

func joinNonEmpty(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(p)
	}
	return b.String()
}

func messageDoc(m store.IndexMessage) map[string]any {
	body := joinNonEmpty(m.Text, jsonText(m.Input), m.Tool)
	if body == "" {
		return nil // images: nothing to find
	}
	d := map[string]any{
		"type": "message", "host": strings.ToLower(m.Host), "provider": m.Provider, "kind": m.Kind, "role": m.Role,
		"session": strconv.FormatInt(m.SessionID, 10), "suuid": strings.ToLower(m.SessionUUID), "body": body,
	}
	if m.Tool != "" {
		d["tool"] = strings.ToLower(m.Tool)
	}
	setTime(d, m.TS)
	return d
}

func sessionDoc(s store.Session) map[string]any {
	kind := "session"
	if s.ParentUUID != "" {
		kind = "subagent"
	}
	d := map[string]any{
		"type": "session", "host": strings.ToLower(s.Host), "provider": s.Provider, "kind": kind,
		"session": strconv.FormatInt(s.ID, 10), "suuid": strings.ToLower(s.UUID),
		"title": joinNonEmpty(s.Title, s.AutoTitle, s.AgentName),
		"body":  joinNonEmpty(s.Cwd, s.Branch, s.Project, s.UUID, s.Path, s.Model, s.Originator),
	}
	setTime(d, s.LastAt, s.FileMtime, s.FirstAt)
	return d
}

func runDoc(r store.Run) map[string]any {
	d := map[string]any{
		"type": "run", "host": strings.ToLower(r.Host), "provider": r.Provider, "kind": r.Kind, "status": r.Status,
		"title": joinNonEmpty(r.Key, r.Target),
		"body":  joinNonEmpty(r.Summary, r.Prompt, r.Log),
	}
	if r.SessionID > 0 {
		d["session"] = strconv.FormatInt(r.SessionID, 10)
	}
	setTime(d, r.StartedAt)
	return d
}

func eventDoc(e store.IndexEvent) map[string]any {
	d := map[string]any{"type": "event", "host": strings.ToLower(e.Host), "kind": e.Level, "body": e.Text}
	setTime(d, e.TS)
	return d
}

// jsonText is the string values of a JSON tool input, one per line, so `"command": "cd x\nmake"` indexes
// as "cd x" and "make", not "nmake". Anything that isn't JSON is returned as it is.
func jsonText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || (s[0] != '{' && s[0] != '[') {
		return s
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return s
	}
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			b.WriteString(t)
			b.WriteByte('\n')
		case json.Number:
			b.WriteString(t.String())
			b.WriteByte('\n')
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		}
	}
	walk(v)
	return b.String()
}
