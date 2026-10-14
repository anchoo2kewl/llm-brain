// Package web is the hub's UI and ship API. Pages are server-rendered with html/template (every transcript
// byte is escaped by the template engine); a little vanilla JS polls for live updates.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/ingest"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	Store    *store.Store
	Engine   *ingest.Engine
	StateDir string // where status.json lives
	Host     string
	Log      *log.Logger

	pages map[string]*template.Template
}

func New(s *store.Store, e *ingest.Engine, stateDir, host string, lg *log.Logger) (*Server, error) {
	srv := &Server{Store: s, Engine: e, StateDir: stateDir, Host: host, Log: lg, pages: map[string]*template.Template{}}
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		base := filepath.Base(n)
		if base == "layout.html" || base == "parts.html" {
			continue
		}
		t, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/parts.html", n)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		srv.pages[strings.TrimSuffix(base, ".html")] = t
	}
	return srv, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.overview)
	mux.HandleFunc("GET /status.json", s.statusJSON)
	mux.HandleFunc("GET /runs", s.runs)
	mux.HandleFunc("GET /runs/{id}", s.run)
	mux.HandleFunc("GET /runs/{id}/log", s.runLog)
	mux.HandleFunc("GET /log/{name}", s.logByName) // the old dashboard's log links
	mux.HandleFunc("GET /sessions", s.sessions)
	mux.HandleFunc("GET /sessions/{id}", s.session)
	mux.HandleFunc("GET /sessions/{id}/more", s.sessionMore)
	mux.HandleFunc("GET /messages/{id}/raw", s.messageRaw)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("GET /hosts", s.hosts)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /api/ship/offsets", s.tailnetOnly(s.shipOffsets))
	mux.HandleFunc("POST /api/ship/chunk", s.tailnetOnly(s.shipChunk))
	mux.HandleFunc("POST /api/ship/heartbeat", s.tailnetOnly(s.shipHeartbeat))
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:")
		h.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- tailnet guard

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// AllowedPeer: loopback or a Tailscale address (100.64.0.0/10, or Tailscale's fd7a:115c:a1e0::/48).
func AllowedPeer(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return a.IsLoopback() || cgnat.Contains(a) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(a)
}

func (s *Server) tailnetOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !AllowedPeer(r.RemoteAddr) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// ---------------------------------------------------------------- ship API

func (s *Server) shipOffsets(w http.ResponseWriter, r *http.Request) {
	m, err := s.Engine.Offsets(r.Context(), r.URL.Query().Get("host"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) shipChunk(w http.ResponseWriter, r *http.Request) {
	c, err := ingest.DecodeChunk(r, ingest.MaxLine+1<<20)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ingest.OffsetReply{Error: err.Error()})
		return
	}
	off, err := s.Engine.Chunk(r.Context(), c)
	switch {
	case errors.Is(err, ingest.ErrOffset):
		writeJSON(w, http.StatusConflict, ingest.OffsetReply{Offset: off, Error: "offset mismatch"})
	case err != nil:
		s.Log.Printf("ship chunk %s %s: %v", c.Host, c.Path, err)
		writeJSON(w, http.StatusInternalServerError, ingest.OffsetReply{Error: err.Error()})
	default:
		writeJSON(w, http.StatusOK, ingest.OffsetReply{Offset: off})
	}
}

func (s *Server) shipHeartbeat(w http.ResponseWriter, r *http.Request) {
	var hb ingest.Heartbeat
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&hb); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Engine.Heartbeat(r.Context(), hb); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------- pages

func (s *Server) render(w http.ResponseWriter, page, tmpl string, data any) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "no template "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := t.ExecuteTemplate(w, tmpl, data); err != nil {
		s.Log.Printf("render %s: %v", page, err)
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.Log.Printf("error: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

type page struct {
	Title string
	Nav   string
	Q     string
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// overview: everything the old dashboard showed (from status.json) plus agents running on every host.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := struct {
		page
		Status     *Status
		StatusErr  string
		Runs       []store.Run
		Live       []store.Session
		Events     []store.Event
		Hosts      []store.Host
		RecentRuns []store.Run
		Now        time.Time
	}{page: page{Title: "Overview", Nav: "overview"}, Now: time.Now()}
	st, err := readStatus(filepath.Join(s.StateDir, "status.json"))
	if err != nil {
		data.StatusErr = err.Error()
	}
	data.Status = st
	var errs []error
	var e error
	data.Runs, e = s.Store.RunningRuns(ctx)
	errs = append(errs, e)
	data.Live, e = s.Store.LiveSessions(ctx)
	errs = append(errs, e)
	data.Events, e = s.Store.Events(ctx, 40)
	errs = append(errs, e)
	data.Hosts, e = s.Store.Hosts(ctx)
	errs = append(errs, e)
	data.RecentRuns, e = s.Store.Runs(ctx, store.RunFilter{Limit: 10})
	errs = append(errs, e)
	if err := errors.Join(errs...); err != nil {
		s.fail(w, err)
		return
	}
	tmpl := "layout"
	if r.URL.Query().Get("fragment") == "1" {
		tmpl = "content"
	}
	s.render(w, "overview", tmpl, data)
}

func (s *Server) statusJSON(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(filepath.Join(s.StateDir, "status.json"))
	if err != nil {
		b = []byte("{}")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.RunFilter{Kind: q.Get("kind"), Status: q.Get("status"), Host: q.Get("host"), Provider: q.Get("provider"), Q: strings.TrimSpace(q.Get("q"))}
	runs, err := s.Store.Runs(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	opts := map[string][]string{}
	for _, c := range []string{"kind", "status", "host", "provider"} {
		opts[c], _ = s.Store.Distinct(r.Context(), "runs", c)
	}
	s.render(w, "runs", "layout", struct {
		page
		F    store.RunFilter
		Runs []store.Run
		Opts map[string][]string
	}{page{Title: "Runs", Nav: "runs", Q: f.Q}, f, runs, opts})
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	run, err := s.Store.Run(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	var sess *store.Session
	if run.SessionID > 0 {
		if x, err := s.Store.Session(r.Context(), run.SessionID); err == nil {
			sess = &x
		}
	}
	s.render(w, "run", "layout", struct {
		page
		Run     store.Run
		Session *store.Session
	}{page{Title: run.Key, Nav: "runs"}, run, sess})
}

// runLog serves the log from a byte offset, for live tailing.
func (s *Server) runLog(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	logText, status, ended, err := s.Store.RunLog(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	if from < 0 || from > len(logText) {
		from = len(logText)
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": logText[from:], "next": len(logText), "status": status, "ended": ended != ""})
}

func (s *Server) logByName(w http.ResponseWriter, r *http.Request) {
	id, err := s.Store.RunByLog(r.Context(), r.URL.Query().Get("host"), r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/runs/"+strconv.FormatInt(id, 10), http.StatusFound)
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.SessionFilter{Host: q.Get("host"), Project: q.Get("project"), Provider: q.Get("provider"), Q: strings.TrimSpace(q.Get("q")), Fleet: q.Get("fleet")}
	list, err := s.Store.Sessions(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	hosts, _ := s.Store.Distinct(r.Context(), "sessions", "host")
	provs, _ := s.Store.Distinct(r.Context(), "sessions", "provider")
	// Group by host, then project, keeping the most recently active group first.
	type group struct {
		Host, Project string
		Sessions      []store.Session
	}
	idx := map[string]int{}
	var groups []*group
	for _, x := range list {
		proj := x.Cwd
		if proj == "" {
			proj = x.Project
		}
		k := x.Host + "\x00" + proj
		i, ok := idx[k]
		if !ok {
			i = len(groups)
			idx[k] = i
			groups = append(groups, &group{Host: x.Host, Project: proj})
		}
		groups[i].Sessions = append(groups[i].Sessions, x)
	}
	s.render(w, "sessions", "layout", struct {
		page
		F         store.SessionFilter
		Groups    []*group
		Count     int
		Hosts     []string
		Providers []string
	}{page{Title: "Sessions", Nav: "sessions", Q: f.Q}, f, groups, len(list), hosts, provs})
}

const pageSize = 400

// item is a transcript block with its tool result folded in.
type item struct {
	store.Message
	Result *store.Message
}

func fold(ms []store.Message) []item {
	var out []item
	uses := map[string]int{}
	for _, m := range ms {
		if m.Kind == "tool_result" && m.ToolUseID != "" {
			if i, ok := uses[m.ToolUseID]; ok && out[i].Result == nil {
				mm := m
				out[i].Result = &mm
				continue
			}
		}
		out = append(out, item{Message: m})
		if m.Kind == "tool_use" && m.ToolUseID != "" {
			uses[m.ToolUseID] = len(out) - 1
		}
	}
	return out
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	sess, err := s.Store.Session(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	ms, err := s.Store.Messages(ctx, id, after, before, pageSize)
	if err != nil {
		s.fail(w, err)
		return
	}
	var firstID, lastID int64
	if len(ms) > 0 {
		firstID, lastID = ms[0].ID, ms[len(ms)-1].ID
	}
	var hasEarlier, hasLater bool
	if firstID > 0 {
		e, _ := s.Store.Messages(ctx, id, 0, firstID, 1)
		hasEarlier = len(e) > 0
		l, _ := s.Store.Messages(ctx, id, lastID, 0, 1)
		hasLater = len(l) > 0
	}
	var subs []store.Session
	var parentID int64
	if sess.ParentUUID == "" {
		subs, _ = s.Store.Subagents(ctx, sess.Host, sess.UUID)
	} else {
		parentID, _ = s.Store.SessionByUUID(ctx, sess.Host, sess.ParentUUID)
	}
	s.render(w, "session", "layout", struct {
		page
		S          store.Session
		Items      []item
		FirstID    int64
		LastID     int64
		HasEarlier bool
		HasLater   bool
		Subagents  []store.Session
		ParentID   int64
	}{page{Title: sess.DisplayTitle(), Nav: "sessions"}, sess, fold(ms), firstID, lastID, hasEarlier, hasLater, subs, parentID})
}

// sessionMore returns transcript items after an id as an HTML fragment, for live sessions.
func (s *Server) sessionMore(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	sess, err := s.Store.Session(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	ms, err := s.Store.Messages(r.Context(), id, after, 0, pageSize)
	if err != nil {
		s.fail(w, err)
		return
	}
	last := after
	if len(ms) > 0 {
		last = ms[len(ms)-1].ID
	}
	w.Header().Set("X-Last-Id", strconv.FormatInt(last, 10))
	w.Header().Set("X-Live", strconv.FormatBool(sess.Live))
	s.render(w, "session", "items", fold(ms))
}

func (s *Server) messageRaw(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	raw, err := s.Store.MessageRaw(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(raw))
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	host := r.URL.Query().Get("host")
	data := struct {
		page
		Host  string
		Hits  []store.Hit
		Runs  []store.Run
		Took  time.Duration
		Hosts []string
	}{page: page{Title: "Search", Nav: "search", Q: q}, Host: host}
	data.Hosts, _ = s.Store.Distinct(r.Context(), "sessions", "host")
	if len(q) >= 2 {
		start := time.Now()
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var err error
		if data.Hits, err = s.Store.Search(ctx, q, host, 200); err != nil {
			s.fail(w, err)
			return
		}
		data.Runs, _ = s.Store.Runs(ctx, store.RunFilter{Q: q, Host: host, Limit: 50})
		data.Took = time.Since(start).Round(time.Millisecond)
	}
	s.render(w, "search", "layout", data)
}

func (s *Server) hosts(w http.ResponseWriter, r *http.Request) {
	hs, err := s.Store.Hosts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].LastSeen > hs[j].LastSeen })
	s.render(w, "hosts", "layout", struct {
		page
		Hosts []store.Host
	}{page{Title: "Hosts", Nav: "hosts"}, hs})
}
