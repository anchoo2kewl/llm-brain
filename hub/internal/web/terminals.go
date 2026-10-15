package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
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
	Host, Provider, Folder, Path, Model, Prompt, WtSlug, WtBase string
}

func (s *Server) newTerminal(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.newTerminalPage(w, r, termForm{Host: q.Get("host"), Provider: q.Get("cli"), Path: q.Get("path")}, "")
}

func (s *Server) newTerminalPage(w http.ResponseWriter, r *http.Request, f termForm, errMsg string) {
	hosts, err := s.Agents.Ctl.Folders(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	type hostOpt struct {
		store.AgentHost
		Live bool
	}
	var hs []hostOpt
	for _, h := range hosts {
		hs = append(hs, hostOpt{h, h.Live()})
		if f.Host == "" && h.Live() && h.Host == s.Host {
			f.Host = h.Host
		}
	}
	if f.Host == "" {
		for _, h := range hs {
			if h.Live {
				f.Host = h.Host
				break
			}
		}
	}
	if f.Provider == "" {
		f.Provider = "claude"
	}
	type cli struct{ Value, Label, Note string }
	clis := []cli{
		{"claude", "Claude Code", "claude --dangerously-skip-permissions"},
		{"codex", "Codex", "codex --dangerously-bypass-approvals-and-sandbox"},
		{"gemini", "Gemini CLI", "gemini --approval-mode yolo"},
		{"shell", "Shell", "your login shell"},
	}
	s.render(w, "terminalnew", "layout", struct {
		page
		F     termForm
		Err   string
		Hosts []hostOpt
		CLIs  []cli
	}{page{Title: "New terminal", Nav: "chats"}, f, errMsg, hs, clis})
}

func (s *Server) newTerminalPost(w http.ResponseWriter, r *http.Request) {
	f := termForm{Host: r.PostFormValue("host"), Provider: r.PostFormValue("cli"), Folder: r.PostFormValue("folder"),
		Path: strings.TrimSpace(r.PostFormValue("path")), Model: strings.TrimSpace(r.PostFormValue("model")),
		Prompt: r.PostFormValue("prompt"), WtSlug: strings.TrimSpace(r.PostFormValue("wt_slug")), WtBase: strings.TrimSpace(r.PostFormValue("wt_base"))}
	req := agent.TermReq{Host: f.Host, Provider: f.Provider, Model: f.Model, Prompt: f.Prompt, WorktreeSlug: f.WtSlug, WorktreeBase: f.WtBase}
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
	// xterm.js writes its theme into a <style> element, and the websocket is same-host ws://.
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; connect-src 'self' ws://"+r.Host+" wss://"+r.Host)
	s.render(w, "terminal", "layout", struct {
		page
		T        store.Terminal
		With     string
		Attach   string
		HostLive bool
		Err      string
	}{page{Title: t.Title, Nav: "chats"}, t, agent.TermProviderName(t.Provider), agent.AttachCommand(t.ID),
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
	aws.SetReadLimit(4 << 20)
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
