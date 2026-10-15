package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Terminals: Claude Code, Codex, Gemini or a shell, running interactively in tmux on a host with every
// permission, shown live in the browser (agent/term.go). The browser's websocket and the host agent's
// websocket meet here: the page opens /terminals/{id}/ws, the hub queues a term_attach with a one-time
// token, and the host agent dials /api/agent/term/{token}; the hub then copies frames between the two.

func (s *Server) terminalRoutes(g, p func(string, http.HandlerFunc), mux *http.ServeMux) {
	g("GET /terminals/new", s.newTerminal)
	p("POST /terminals/new", s.newTerminalPost)
	g("GET /terminals/{id}", s.terminalPage)
	g("GET /terminals/{id}/status", s.terminalStatus)
	g("GET /workspace", s.workspace)
	g("GET /edit", s.editorPage)
	p("GET /fs/ws", s.fsWS)
	g("GET /api/terminals", s.terminalsAPI)
	g("GET /api/live-sessions", s.liveSessionsAPI)
	p("POST /terminals/{id}/rename", s.terminalRename)
	// Driving a terminal from a script or an agent: type / press keys, read the screen.
	p("POST /api/terminals/{id}/send", s.terminalSend)
	g("GET /api/terminals/{id}/screen", s.terminalScreen)
	p("GET /terminals/{id}/ws", s.terminalWS) // sameSite: a websocket from another site is refused
	p("POST /terminals/{id}/kill", s.terminalKill)
	p("POST /terminals/{id}/review", s.terminalReview)
	// The host agent's side: tailnet-only, and the token is the only key.
	mux.HandleFunc("GET /api/agent/term/{token}", s.tailnetOnly(s.agentTermWS))
}

// bridges holds browser websockets waiting for their host agent to dial in, by token.
type bridges struct {
	mu sync.Mutex
	m  map[string]*bridge
}

type bridge struct {
	agent chan *websocket.Conn // the agent's connection, handed over once
	done  chan struct{}        // closed when the browser side is finished with it
}

func (b *bridges) add(tok string) *bridge {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]*bridge{}
	}
	br := &bridge{agent: make(chan *websocket.Conn, 1), done: make(chan struct{})}
	b.m[tok] = br
	return br
}

func (b *bridges) take(tok string) *bridge {
	b.mu.Lock()
	defer b.mu.Unlock()
	br := b.m[tok]
	delete(b.m, tok)
	return br
}

func token() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------- pages

type termForm struct {
	Host, Provider, Folder, Path, Model, Prompt, WtSlug, WtBase, Perm string
}

type cli struct{ Value, Label, Note string }

var termCLIs = []cli{{"claude", "Claude Code", "claude"}, {"codex", "Codex", "codex"}, {"gemini", "Gemini CLI", "gemini"}, {"shell", "Shell", "your login shell"}}

// pick is a folder the picker offers: Value is what goes in the box ("~/elastio"), Label how it reads.
type pick struct{ Value, Label string }

// tilde writes p relative to that host's home ("~/elastio"), so the same path reads the same on every Mac.
func tilde(home, p string) string {
	switch {
	case home == "":
		return p
	case p == home:
		return "~"
	case strings.HasPrefix(p, home+"/"):
		return "~/" + strings.TrimPrefix(p, home+"/")
	}
	return p
}

func (s *Server) newTerminal(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.newTerminalPage(w, r, termForm{Host: q.Get("host"), Provider: q.Get("cli"), Path: q.Get("path")}, "")
}

// termHost is a Mac in the folder picker: its quick picks, recent folders and every folder it can suggest.
type termHost struct {
	store.AgentHost
	Live          bool
	Quick, Recent []pick
	All           []pick // the datalist: quick, recent, every folder in home, every repo
}

// termHosts builds the folder picker for every Mac, and picks the default host into *sel.
func (s *Server) termHosts(ctx context.Context, sel *string) ([]termHost, error) {
	hosts, err := s.Agents.Ctl.Folders(ctx)
	if err != nil {
		return nil, err
	}
	var hs []termHost
	for _, h := range hosts {
		o := termHost{AgentHost: h, Live: h.Live()}
		home, places, _ := s.Store.AgentPlaces(ctx, h.Host)
		seen := map[string]bool{}
		add := func(list *[]pick, p, label string) {
			v := tilde(home, p)
			if label == "" {
				label = v
			}
			*list = append(*list, pick{v, label})
			if !seen[v] {
				seen[v] = true
				o.All = append(o.All, pick{v, label})
			}
		}
		if home != "" {
			add(&o.Quick, home, "~ home")
		}
		for _, root := range h.Roots { // where the repos live (~/elastio), then the usual code folders
			add(&o.Quick, root, "")
		}
		for _, p := range places {
			switch filepath.Base(p) {
			case "projects", "code", "src", "dev", "work", "repos", "git":
				if !seen[tilde(home, p)] {
					add(&o.Quick, p, "")
				}
			}
		}
		recent, _ := s.Store.RecentFolders(ctx, h.Host, home, 40)
		for _, p := range recent {
			v := tilde(home, p)
			// Skip the fleet's own scratch (inside dot-folders) and what the quick picks already offer.
			if seen[v] || strings.Contains(v, "/.") || len(o.Recent) >= 8 {
				continue
			}
			add(&o.Recent, p, "")
		}
		var rest []pick
		for _, p := range places {
			add(&rest, p, "")
		}
		for _, fo := range h.Folders {
			label := tilde(home, fo.Path)
			if fo.Branch != "" {
				label += " (" + fo.Branch + ")"
			}
			add(&rest, fo.Path, label)
		}
		hs = append(hs, o)
		if *sel == "" && o.Live && h.Host == s.Host {
			*sel = h.Host
		}
	}
	if *sel == "" {
		for _, h := range hs {
			if h.Live {
				*sel = h.Host
				break
			}
		}
	}
	return hs, nil
}

func (s *Server) newTerminalPage(w http.ResponseWriter, r *http.Request, f termForm, errMsg string) {
	hs, err := s.termHosts(r.Context(), &f.Host)
	if err != nil {
		s.fail(w, err)
		return
	}
	if f.Provider == "" {
		f.Provider = "claude"
	}
	if f.Perm != "ask" {
		f.Perm = "full"
	}
	s.render(w, "terminalnew", "layout", struct {
		page
		F     termForm
		Err   string
		Hosts []termHost
		CLIs  []cli
	}{page{Title: "New terminal", Nav: "new"}, f, errMsg, hs, termCLIs})
}

func (s *Server) newTerminalPost(w http.ResponseWriter, r *http.Request) {
	f := termForm{Host: r.PostFormValue("host"), Provider: r.PostFormValue("cli"), Folder: r.PostFormValue("folder"),
		Path: strings.TrimSpace(r.PostFormValue("path")), Model: strings.TrimSpace(r.PostFormValue("model")),
		Prompt: r.PostFormValue("prompt"), WtSlug: strings.TrimSpace(r.PostFormValue("wt_slug")), WtBase: strings.TrimSpace(r.PostFormValue("wt_base")),
		Perm: r.PostFormValue("perm")}
	req := agent.TermReq{Host: f.Host, Provider: f.Provider, Model: f.Model, Prompt: f.Prompt, WorktreeSlug: f.WtSlug, WorktreeBase: f.WtBase,
		Ask: f.Perm == "ask"}
	switch {
	case f.Path != "": // a typed path wins over the list
		req.Cwd = f.Path
	case f.Folder != "":
		host, cwd, _ := strings.Cut(f.Folder, "|")
		if host != f.Host {
			s.newTerminalPage(w, r, f, "that folder is on "+host+"; pick a folder on "+f.Host)
			return
		}
		req.Cwd = cwd
	}
	id, err := s.Agents.Ctl.OpenTerminal(r.Context(), req)
	if r.FormValue("format") == "json" { // the workspace's quick "new terminal"
		if err != nil {
			msg, ok := isInput(err)
			if !ok {
				msg = "internal error"
				s.Log.Printf("new terminal: %v", err)
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"id": id})
		return
	}
	if err != nil {
		if msg, ok := isInput(err); ok {
			s.newTerminalPage(w, r, f, msg)
			return
		}
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, idPath(id, "/terminals/%d"), http.StatusSeeOther)
}

func (s *Server) terminalPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	t, err := s.Store.Terminal(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.markSeen(r.Context(), "t:"+strconv.FormatInt(id, 10))
	// xterm.js writes its theme into a <style> element, and the websocket is same-host ws://.
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; connect-src 'self' ws://"+r.Host+" wss://"+r.Host)
	s.render(w, "terminal", "layout", struct {
		page
		T        store.Terminal
		With     string
		Attach   string
		HostLive bool
		Err      string
	}{page{Title: t.Title, Nav: "now"}, t, agent.TermProviderName(t.Provider), agent.AttachCommand(t.ID),
		s.Agents.Ctl.HostLive(r.Context(), t.Host), r.URL.Query().Get("err")})
}

func (s *Server) terminalStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	t, err := s.Store.Terminal(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": t.Status, "error": t.Error, "cwd": t.Cwd,
		"host_live": s.Agents.Ctl.HostLive(r.Context(), t.Host)})
}

func (s *Server) terminalKill(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.back(w, r, idPath(id, "/terminals/%d"), s.Agents.Ctl.KillTerminal(r.Context(), id))
}

func (s *Server) terminalReview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	rid, err := s.Agents.Ctl.ReviewTerminal(r.Context(), id, r.PostFormValue("with"))
	if err != nil {
		s.back(w, r, idPath(id, "/terminals/%d"), err)
		return
	}
	http.Redirect(w, r, idPath(rid, "/agents/%d"), http.StatusSeeOther)
}

// ---------------------------------------------------------------- the bridge

func atoiDef(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// terminalWS is the browser's side. Close reasons the page shows: anything sent with code 4000.
func (s *Server) terminalWS(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	t, err := s.Store.Terminal(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	bws, err := websocket.Accept(w, r, nil) // checks Origin against Host
	if err != nil {
		return
	}
	bws.SetReadLimit(4 << 20)
	closeWith := func(msg string) { _ = bws.Close(4000, truncReason(msg)) }
	if t.Status != "live" {
		closeWith("the terminal is " + t.Status)
		return
	}
	if !s.Agents.Ctl.HostLive(ctx, t.Host) {
		closeWith("the agent on " + t.Host + " is offline")
		return
	}
	tok := token()
	br := s.Agents.bridges.add(tok)
	defer func() {
		s.Agents.bridges.take(tok)
		close(br.done)
	}()
	q := r.URL.Query()
	cid, err := s.Agents.Ctl.Attach(ctx, t, tok, atoiDef(q.Get("cols"), 120), atoiDef(q.Get("rows"), 40))
	if err != nil {
		closeWith(err.Error())
		return
	}
	var aws *websocket.Conn
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(30 * time.Second)
wait:
	for {
		select {
		case aws = <-br.agent:
			break wait
		case <-ctx.Done():
			return
		case <-deadline:
			closeWith("the agent on " + t.Host + " didn't answer")
			return
		case <-tick.C:
			if c, err := s.Store.Command(ctx, cid); err == nil && c.Status == "failed" {
				msg := "attach failed"
				if e := reportError(c.Result); e != "" {
					msg = e
				}
				closeWith(msg)
				return
			}
		}
	}
	_ = s.Store.TouchTerminal(ctx, id)
	pipe(ctx, bws, aws)
}

// agentTermWS is the host agent's side: hand its connection to the waiting browser, then hold the handler
// until the browser side is done with it.
func (s *Server) agentTermWS(w http.ResponseWriter, r *http.Request) {
	br := s.Agents.bridges.take(r.PathValue("token"))
	if br == nil {
		http.NotFound(w, r)
		return
	}
	aws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	// Closed here whatever happens: a browser that gave up before this handoff never reads it.
	defer aws.CloseNow()
	aws.SetReadLimit(32 << 20) // editor files are up to 8 MB, JSON-escaped
	select {
	case br.agent <- aws:
	case <-br.done:
		return
	}
	select {
	case <-br.done:
	case <-r.Context().Done():
	}
}

// pipe copies frames both ways until one side closes, and passes the close reason on.
func pipe(ctx context.Context, a, b *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var once sync.Once
	end := func(code websocket.StatusCode, why string) {
		once.Do(func() {
			cancel()
			_ = a.Close(code, why)
			_ = b.Close(code, why)
		})
	}
	cp := func(from, to *websocket.Conn) {
		for {
			typ, data, err := from.Read(ctx)
			if err != nil {
				code, why := websocket.StatusNormalClosure, ""
				var ce websocket.CloseError
				if errors.As(err, &ce) {
					code, why = ce.Code, ce.Reason
				}
				switch {
				case why != "":
					code = 4000 // the page shows 4000 reasons
				case code != websocket.StatusNormalClosure && code != websocket.StatusGoingAway:
					code = websocket.StatusNormalClosure // 1005/1006 can't be sent
				}
				end(code, why)
				return
			}
			if err := to.Write(ctx, typ, data); err != nil {
				end(websocket.StatusGoingAway, "")
				return
			}
		}
	}
	go cp(b, a)
	cp(a, b)
	<-ctx.Done()
}

func truncReason(s string) string {
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

func reportError(result string) string {
	var r agent.Report
	_ = json.Unmarshal([]byte(result), &r)
	return r.Error
}

// ---------------------------------------------------------------- workspace

// termJSON is a terminal for the workspace's list.
type termJSON struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Host     string `json:"host"`
	CLI      string `json:"cli"`
	Provider string `json:"provider"`
	Cwd      string `json:"cwd"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
	Updated  string `json:"updated"`
}

// terminalsAPI: open terminals, plus ones that ended in the last day (so a pane can say why it stopped).
func (s *Server) terminalsAPI(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Store.Terminals(r.Context(), "", 200)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	cut := store.T(time.Now().Add(-24 * time.Hour))
	out := []termJSON{}
	for _, t := range ts {
		if !t.Open() && t.UpdatedAt < cut {
			continue
		}
		home, _, _ := s.Store.AgentPlaces(r.Context(), t.Host)
		out = append(out, termJSON{t.ID, t.Title, t.Host, agent.TermProviderName(t.Provider), t.Provider, tilde(home, t.Cwd), t.Status, t.Error, t.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) terminalRename(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	title, err := cleanName(r.FormValue("title"))
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.rename(r.Context(), "terminal", id, title); err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"title": title})
}

func (s *Server) workspace(w http.ResponseWriter, r *http.Request) {
	f := termForm{Provider: "claude", Perm: "full"}
	hs, err := s.termHosts(r.Context(), &f.Host)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; connect-src 'self' ws://"+r.Host+" wss://"+r.Host)
	s.render(w, "workspace", "layout", struct {
		page
		F     termForm
		Hosts []termHost
		CLIs  []cli
	}{page{Title: "Workspace", Nav: "workspace"}, f, hs, termCLIs})
}

// termCommand queues a command for a live terminal and waits (up to 20 s) for its report.
func (s *Server) termCommand(ctx context.Context, id int64, kind string, payload any) (agent.Report, error) {
	t, err := s.Store.Terminal(ctx, id)
	if err != nil {
		return agent.Report{}, err
	}
	if t.Status != "live" {
		return agent.Report{}, fmt.Errorf("the terminal is %s", t.Status)
	}
	if !s.Agents.Ctl.HostLive(ctx, t.Host) {
		return agent.Report{}, fmt.Errorf("the agent on %s is offline", t.Host)
	}
	b, _ := json.Marshal(payload)
	cid, err := s.Store.QueueCommand(ctx, store.Command{Host: t.Host, Kind: kind, AgentID: id, Payload: b})
	if err != nil {
		return agent.Report{}, err
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		c, err := s.Store.Command(ctx, cid)
		if err == nil && store.FinalStatus(c.Status) {
			var r agent.Report
			_ = json.Unmarshal([]byte(c.Result), &r)
			if c.Status != "done" {
				if r.Error == "" {
					r.Error = c.Status
				}
				return r, errors.New(r.Error)
			}
			return r, nil
		}
		select {
		case <-ctx.Done():
			return agent.Report{}, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return agent.Report{}, errors.New("the agent didn't answer in 20 s")
}

// terminalSend types text and/or presses keys. JSON {"text": "...", "keys": ["Enter"]}, or form fields
// text= and keys= (comma-separated).
func (s *Server) terminalSend(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var req agent.TermSend
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			jsonErr(w, http.StatusBadRequest, err)
			return
		}
	} else {
		req.Text = r.FormValue("text")
		for _, k := range strings.Split(r.FormValue("keys"), ",") {
			if k = strings.TrimSpace(k); k != "" {
				req.Keys = append(req.Keys, k)
			}
		}
	}
	if req.Text == "" && len(req.Keys) == 0 {
		jsonErr(w, http.StatusBadRequest, errors.New("nothing to send: give text and/or keys"))
		return
	}
	if len(req.Text) > 64<<10 || len(req.Keys) > 50 {
		jsonErr(w, http.StatusBadRequest, errors.New("too much at once"))
		return
	}
	if _, err := s.termCommand(r.Context(), id, agent.KindTermSend, req); err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// terminalScreen returns the visible screen as text (?scrollback=N adds N lines above it).
func (s *Server) terminalScreen(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("scrollback"))
	rep, err := s.termCommand(r.Context(), id, agent.KindTermCapture, agent.TermCapture{Scrollback: n})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	if r.URL.Query().Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, rep.Result)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"screen": rep.Result})
}

// liveSessionsAPI: Claude/Codex sessions running anywhere (a terminal on the laptop, an IDE), not just the
// hub's terminals, for the workspace's "Running elsewhere". Sessions that are a live hub terminal's own
// (same Mac, folder and CLI) are left out.
func (s *Server) liveSessionsAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ss, err := s.Store.LiveSessions(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	// Each open hub terminal claims one session: same Mac, folder and CLI, the first one to start after the
	// terminal was opened. Every other session is somebody else's, even in the same folder.
	ts, _ := s.Store.Terminals(ctx, "", 200)
	mine := map[int64]bool{}
	for _, t := range ts {
		if !t.Open() {
			continue
		}
		var best *store.Session
		for i := range ss {
			x := &ss[i]
			if mine[x.ID] || x.ParentUUID != "" || x.Host != t.Host || x.Cwd != t.Cwd || x.Provider != t.Provider || x.FirstAt < t.CreatedAt {
				continue
			}
			if best == nil || x.FirstAt < best.FirstAt {
				best = x
			}
		}
		if best != nil {
			mine[best.ID] = true
		}
	}
	type liveJSON struct {
		ID       int64  `json:"id"`
		Title    string `json:"title"`
		Host     string `json:"host"`
		CLI      string `json:"cli"`
		Cwd      string `json:"cwd"`
		Updated  string `json:"updated"`
		Messages int    `json:"messages"`
	}
	out := []liveJSON{}
	for _, x := range ss {
		if x.ParentUUID != "" || mine[x.ID] {
			continue
		}
		home, _, _ := s.Store.AgentPlaces(ctx, x.Host)
		out = append(out, liveJSON{x.ID, x.DisplayTitle(), x.Host, agent.TermProviderName(x.Provider), tilde(home, x.Cwd), x.FileMtime, int(x.MessageCount)})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------- editor

// editorPage: Monaco on a folder of a Mac (?host=&root=), files over a live channel (agent/fsbridge.go).
func (s *Server) editorPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	host, root := q.Get("host"), q.Get("root")
	if host == "" {
		host = s.Host
	}
	if root == "" {
		root = "~"
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; worker-src 'self' blob:; font-src 'self' data:; img-src 'self' data:; connect-src 'self' ws://"+r.Host+" wss://"+r.Host)
	s.render(w, "editor", "layout", struct {
		page
		Host, Root, File string
		Live             bool
	}{page{Title: "Edit " + root, Nav: "workspace", Embed: q.Get("embed") == "1"}, host, root, q.Get("file"), s.Agents.Ctl.HostLive(r.Context(), host)})
}

// fsWS bridges the editor to the host's file channel, the same way a terminal is bridged.
func (s *Server) fsWS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	host, root := r.URL.Query().Get("host"), r.URL.Query().Get("root")
	bws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	bws.SetReadLimit(32 << 20)
	closeWith := func(msg string) { _ = bws.Close(4000, truncReason(msg)) }
	if !s.Agents.Ctl.HostLive(ctx, host) {
		closeWith("the agent on " + host + " is offline")
		return
	}
	tok := token()
	br := s.Agents.bridges.add(tok)
	defer func() {
		s.Agents.bridges.take(tok)
		close(br.done)
	}()
	b, _ := json.Marshal(agent.FSAttach{Token: tok, Root: root})
	cid, err := s.Store.QueueCommand(ctx, store.Command{Host: host, Kind: agent.KindFSAttach, Payload: b})
	if err != nil {
		closeWith(err.Error())
		return
	}
	var aws *websocket.Conn
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(30 * time.Second)
wait:
	for {
		select {
		case aws = <-br.agent:
			break wait
		case <-ctx.Done():
			return
		case <-deadline:
			closeWith("the agent on " + host + " didn't answer")
			return
		case <-tick.C:
			if c, err := s.Store.Command(ctx, cid); err == nil && c.Status == "failed" {
				msg := reportError(c.Result)
				if msg == "" {
					msg = "couldn't open the folder"
				}
				closeWith(msg)
				return
			}
		}
	}
	pipe(ctx, bws, aws)
}
