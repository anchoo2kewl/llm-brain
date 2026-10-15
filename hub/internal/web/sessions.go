package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
	"github.com/anchoo2kewl/llm-brain/hub/internal/llm"
	"github.com/anchoo2kewl/llm-brain/hub/internal/search"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Session control and chats: start Claude/Codex/Gemini sessions on any host with a live agent, continue,
// stop and cross-review them; plain chats with any model the account gateway serves. Every route here is
// tailnet-only, and every POST is refused when it comes from another site.

// Sessions is what these pages need; nil turns them off.
type Sessions struct {
	Ctl   *agent.Control
	Hub   *agent.LocalHub
	Chats *llm.Chats // nil: no plain chat (no gateway)
}

func (s *Server) sessionRoutes(mux *http.ServeMux) {
	if s.Agents == nil {
		return
	}
	g := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, s.tailnetOnly(h)) }
	p := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, s.tailnetOnly(s.sameSite(h))) }
	// host agents
	p("POST /api/agent/poll", s.apiPoll) // claims commands: a POST, JSON only, so no page can trigger it
	p("POST /api/agent/commands/{id}", s.apiReport)
	p("POST /api/agent/inventory", s.apiInventory)
	// chats
	g("GET /chats", s.chatsList)
	g("GET /chats/new", s.newChat)
	p("POST /chats/new", s.newChatPost)
	g("GET /agents", s.agentsPage)
	g("GET /agents/{id}", s.agentPage)
	g("GET /agents/{id}/conversation", s.agentConversation)
	p("POST /agents/{id}/send", s.agentSend)
	p("POST /agents/{id}/stop", s.agentStop)
	p("POST /agents/{id}/review", s.agentReview)
	p("POST /agents/{id}/sendback", s.agentSendBack)
	g("GET /sessions/{id}/agent", s.sessionAgent)
	p("POST /sessions/{id}/send", s.sessionSend)
	p("POST /sessions/{id}/review", s.sessionReview)
	g("GET /llms/{id}", s.llmPage)
	p("POST /llms/{id}/send", s.llmSend)
	g("GET /llms/{id}/events", s.llmEvents)
	g("GET /prompts", s.promptsPage)
	p("POST /prompts", s.promptSave)
	p("POST /prompts/{id}/delete", s.promptDelete)
}

// sameSite wraps sameOrigin (chat.go) for every POST here.
func (s *Server) sameSite(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func isInput(err error) (string, bool) {
	var ie agent.InputError
	if errors.As(err, &ie) {
		return ie.Msg, true
	}
	var le llm.InputError
	if errors.As(err, &le) {
		return string(le), true
	}
	return "", false
}

// back redirects to a page, with err shown there when it is the person's mistake.
func (s *Server) back(w http.ResponseWriter, r *http.Request, to string, err error) {
	if err != nil {
		msg, ok := isInput(err)
		if !ok {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			s.Log.Printf("sessions: %v", err)
			msg = "internal error"
		}
		path, frag, _ := strings.Cut(to, "#")
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		to = path + sep + "err=" + url.QueryEscape(msg)
		if frag != "" {
			to += "#" + frag
		}
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func idPath(id int64, f string) string { return fmt.Sprintf(f, id) }

// ---------------------------------------------------------------- host agent API

func (s *Server) apiPoll(w http.ResponseWriter, r *http.Request) {
	var req agent.PollRequest
	if !jsonBody(w, r, &req) {
		return
	}
	cs, err := s.Agents.Hub.Poll(r.Context(), req.Host, req.Version, req.Running)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if cs == nil {
		cs = []store.Command{}
	}
	writeJSON(w, http.StatusOK, cs)
}

// jsonBody decodes a JSON request; a form a browser can send cross-site without asking is refused.
func jsonBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		http.Error(w, "want application/json", http.StatusUnsupportedMediaType)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) apiReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var rep agent.Report
	if !jsonBody(w, r, &rep) {
		return
	}
	if err := s.Agents.Hub.Report(r.Context(), id, rep); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) apiInventory(w http.ResponseWriter, r *http.Request) {
	var inv agent.Inventory
	if !jsonBody(w, r, &inv) {
		return
	}
	if err := s.Agents.Hub.Inventory(r.Context(), inv); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- chat list

type chatRow struct {
	Kind, Link, Title, With, Host, Status, Updated string
	Review                                         bool
}

func agentRow(a store.AgentSession) chatRow {
	t := a.Title
	if t == "" {
		t = "(untitled)"
	}
	return chatRow{Kind: "agent", Link: idPath(a.ID, "/agents/%d"), Title: t, With: agent.ProviderName(a.Provider), Host: a.Host,
		Status: a.Status, Updated: a.UpdatedAt, Review: a.ReviewOf > 0}
}

func (s *Server) chatsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	as, err := s.Store.SearchAgentSessions(ctx, q, 200)
	if err != nil {
		s.fail(w, err)
		return
	}
	var rows []chatRow
	for _, a := range as {
		rows = append(rows, agentRow(a))
	}
	if s.Agents.Chats != nil {
		cs, err := s.plainChats(ctx, q)
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, c := range cs {
			rows = append(rows, chatRow{Kind: "plain", Link: idPath(c.ID, "/llms/%d"), Title: c.Title, With: c.Model, Host: "gateway",
				Status: "chat", Updated: c.UpdatedAt})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Updated > rows[j].Updated })
	s.render(w, "chats", "layout", struct {
		page
		Rows []chatRow
	}{page{Title: "Chats", Nav: "chats", Q: q}, rows})
}

// plainChats finds plain chats for the list: by title and model, and by message text through the search
// index (or, until it is built, a scan of the newest messages only).
func (s *Server) plainChats(ctx context.Context, q string) ([]store.LLMChat, error) {
	indexed := q != "" && s.Index != nil && s.Index.Ready()
	cs, err := s.Store.LLMChats(ctx, q, 200, q != "" && !indexed)
	if err != nil || !indexed {
		return cs, err
	}
	res, err := s.Index.Search(ctx, search.Request{Q: q, Type: "llm", Size: 200})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(res.Hits))
	for _, h := range res.Hits {
		ids = append(ids, h.ID)
	}
	more, err := s.Store.LLMChatsWithMessages(ctx, ids)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	for _, c := range cs {
		seen[c.ID] = true
	}
	for _, c := range more {
		if !seen[c.ID] {
			cs = append(cs, c)
		}
	}
	return cs, nil
}

// ---------------------------------------------------------------- new chat

type pickOpt struct{ Value, Label string }

type pickGroup struct {
	Label string
	Opts  []pickOpt
}

type newChatForm struct {
	With, Host, Folder, Model, Perm, Prompt, Vars, WtBase, WtSlug string
	Bypass                                                        bool
}

func (s *Server) newChat(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := newChatForm{Host: q.Get("host"), Folder: q.Get("folder"), Prompt: q.Get("prompt")}
	if id, err := strconv.ParseInt(q.Get("saved"), 10, 64); err == nil {
		if p, err := s.Store.SavedPrompt(r.Context(), id); err == nil {
			f.Prompt = p.Body
		}
	}
	s.newChatPage(w, r, f, q.Get("err"))
}

func (s *Server) newChatPage(w http.ResponseWriter, r *http.Request, f newChatForm, errMsg string) {
	ctx := r.Context()
	hosts, err := s.Store.AgentHosts(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	var groups []pickGroup
	var gwErr string
	if s.Agents.Chats != nil {
		ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
		ms, err := s.Agents.Chats.C.Models(ctx2)
		cancel()
		if err != nil {
			gwErr = err.Error()
		}
		for _, g := range llm.Grouped(ms) {
			pg := pickGroup{Label: "Plain chat · " + g.Vendor}
			for _, m := range g.Models {
				pg.Opts = append(pg.Opts, pickOpt{"plain:" + m.ID, m.ID})
			}
			groups = append(groups, pg)
		}
	} else {
		gwErr = "plain chat is off: the hub has no account gateway (--gateway)"
	}
	// Agent CLIs: whatever any live host has.
	have := map[string]bool{}
	for _, h := range hosts {
		if h.Live() {
			for _, p := range h.Providers {
				have[p] = true
			}
		}
	}
	ag := pickGroup{Label: "Agent · tools in a folder"}
	for _, p := range agent.Providers {
		if have[p] {
			ag.Opts = append(ag.Opts, pickOpt{"agent:" + p, agent.ProviderName(p)})
		}
	}
	if len(ag.Opts) > 0 {
		groups = append([]pickGroup{ag}, groups...)
	}
	if f.With == "" {
		f.With, _ = s.Store.Setting(ctx, store.KeyChatLast)
	}
	found := false
	for _, g := range groups {
		for _, o := range g.Opts {
			found = found || o.Value == f.With
		}
	}
	if !found && len(groups) > 0 {
		f.With = groups[0].Opts[0].Value
	}
	if f.Host == "" {
		f.Host = s.Host
	}
	prompts, _ := s.Store.SavedPrompts(ctx)
	s.render(w, "chatnew", "layout", struct {
		page
		F       newChatForm
		Groups  []pickGroup
		GwErr   string
		Err     string
		Hosts   []store.AgentHost
		Prompts []store.SavedPrompt
	}{page{Title: "New chat", Nav: "chats"}, f, groups, gwErr, errMsg, hosts, prompts})
}

func (s *Server) newChatPost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newChatForm{With: r.PostFormValue("with"), Host: r.PostFormValue("host"), Folder: r.PostFormValue("folder"),
		Model: strings.TrimSpace(r.PostFormValue("model")), Perm: r.PostFormValue("perm"), Prompt: r.PostFormValue("prompt"),
		Vars: r.PostFormValue("vars"), WtBase: strings.TrimSpace(r.PostFormValue("wt_base")), WtSlug: strings.TrimSpace(r.PostFormValue("wt_slug")),
		Bypass: r.PostFormValue("bypass") == "1"}
	kind, val, _ := strings.Cut(f.With, ":")
	fail := func(err error) {
		msg, ok := isInput(err)
		if !ok {
			s.Log.Printf("new chat: %v", err)
			msg = "internal error"
		}
		s.newChatPage(w, r, f, msg)
	}
	switch kind {
	case "plain":
		if s.Agents.Chats == nil {
			fail(llm.InputError("plain chat is off on this hub"))
			return
		}
		id, _, err := s.Agents.Chats.Start(ctx, val, f.Prompt)
		if err != nil {
			fail(err)
			return
		}
		_ = s.Store.SetSettings(ctx, map[string]string{store.KeyChatLast: f.With})
		http.Redirect(w, r, idPath(id, "/llms/%d"), http.StatusSeeOther)
	case "agent":
		host, cwd := f.Host, ""
		if f.Folder != "" {
			host, cwd, _ = strings.Cut(f.Folder, "|")
		}
		perm := f.Perm
		if perm == "default" {
			perm = ""
		}
		id, err := s.Agents.Ctl.Start(ctx, agent.StartReq{Host: host, Cwd: cwd, Provider: val, Model: f.Model, Perm: perm, Bypass: f.Bypass,
			WorktreeBase: f.WtBase, WorktreeSlug: f.WtSlug, Prompt: f.Prompt, Vars: agent.ParseVars(f.Vars)})
		if err != nil {
			fail(err)
			return
		}
		_ = s.Store.SetSettings(ctx, map[string]string{store.KeyChatLast: f.With})
		http.Redirect(w, r, idPath(id, "/agents/%d"), http.StatusSeeOther)
	default:
		fail(llm.InputError("pick who to chat with"))
	}
}

// ---------------------------------------------------------------- agents

func (s *Server) agentsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hosts, err := s.Store.AgentHosts(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	as, err := s.Store.AgentSessions(ctx, 300)
	if err != nil {
		s.fail(w, err)
		return
	}
	cmds, err := s.Store.Commands(ctx, 0, 60)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "agents", "layout", struct {
		page
		Hosts    []store.AgentHost
		Sessions []store.AgentSession
		Commands []store.Command
		Err      string
	}{page{Title: "Agents", Nav: "chats"}, hosts, as, cmds, r.URL.Query().Get("err")})
}

type agentView struct {
	A          store.AgentSession
	PermLabel  string
	With       string
	Other      string
	OtherName  string
	Resume     string
	SessionID  int64 // the ingested transcript
	RunID      int64 // the latest turn's log
	HostLive   bool
	Reviews    []store.AgentSession
	ReviewOf   *store.AgentSession
	CanSend    bool // a review with findings that can go back
	Convo      template.HTML
	Err        string
	Prompts    []store.SavedPrompt
	Scratch    bool
	BusyPrompt string
}

func (s *Server) agentView(ctx context.Context, id int64) (agentView, error) {
	a, err := s.Store.AgentSession(ctx, id)
	if err != nil {
		return agentView{}, err
	}
	v := agentView{A: a, PermLabel: agent.PermLabel(a.Provider, a.Perm), With: agent.ProviderName(a.Provider), Other: agent.Other(a.Provider),
		Resume: agent.ResumeCommand(a.Provider, a.Cwd, a.ProviderSession), HostLive: s.Agents.Ctl.HostLive(ctx, a.Host), Scratch: a.Scratch}
	v.OtherName = agent.ProviderName(v.Other)
	if a.ProviderSession != "" {
		v.SessionID, _ = s.Store.SessionByUUID(ctx, a.Host, a.ProviderSession)
	}
	v.RunID, _ = s.Store.LatestRunByKey(ctx, a.Host, "agent-"+strconv.FormatInt(a.ID, 10))
	v.Reviews, _ = s.Store.Reviews(ctx, a.ID)
	if a.ReviewOf > 0 {
		if o, err := s.Store.AgentSession(ctx, a.ReviewOf); err == nil {
			v.ReviewOf = &o
			v.CanSend = !a.Busy() && strings.TrimSpace(a.LastResult) != ""
		}
	}
	v.Convo, err = s.conversation(ctx, a, v.SessionID, v.RunID)
	return v, err
}

func (s *Server) agentPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	v, err := s.agentView(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	v.Err = r.URL.Query().Get("err")
	v.Prompts, _ = s.Store.SavedPrompts(r.Context())
	s.render(w, "agent", "layout", struct {
		page
		V agentView
	}{page{Title: v.A.Title, Nav: "chats"}, v})
}

// conversationTurns is how many transcript blocks a chat page shows.
const conversationTurns = 200

type turnView struct {
	Prompt string
	Reply  template.HTML
	Status string
	Error  string
}

// conversation renders the chat: the ingested transcript when there is one, else the turns the hub ran
// (prompt, final answer), and while a turn runs, its live output.
func (s *Server) conversation(ctx context.Context, a store.AgentSession, sessionID, runID int64) (template.HTML, error) {
	var buf bytes.Buffer
	data := struct {
		A        store.AgentSession
		Items    template.HTML
		Earlier  bool
		Session  int64
		Turns    []turnView
		Busy     bool
		LiveLog  string
		RunID    int64
		WithName string
	}{A: a, Session: sessionID, Busy: a.Busy(), RunID: runID, WithName: agent.ProviderName(a.Provider)}
	if sessionID > 0 {
		ms, err := s.Store.Messages(ctx, sessionID, 0, 0, conversationTurns)
		if err != nil {
			return "", err
		}
		data.Earlier = len(ms) == conversationTurns
		var ib bytes.Buffer
		if err := s.pages["agent"].ExecuteTemplate(&ib, "items", fold(ms)); err != nil {
			return "", err
		}
		data.Items = template.HTML(ib.String()) //nolint:gosec // rendered by html/template
	}
	if sessionID == 0 || data.Busy {
		cmds, err := s.Store.Commands(ctx, a.ID, 100)
		if err != nil {
			return "", err
		}
		for i := len(cmds) - 1; i >= 0; i-- {
			c := cmds[i]
			if c.Kind != agent.KindTurn {
				continue
			}
			var t agent.Turn
			_ = json.Unmarshal(c.Payload, &t)
			var rep agent.Report
			_ = json.Unmarshal([]byte(c.Result), &rep)
			tv := turnView{Prompt: t.Prompt, Status: c.Status, Error: rep.Error}
			if t.Review != nil && t.Resume == "" {
				tv.Prompt = "Review the change in this folder" + map[bool]string{true: " against " + t.Review.Base, false: ""}[t.Review.Base != ""] + "."
			}
			if rep.Result != "" {
				tv.Reply = llm.Markdown(rep.Result)
			}
			// With a transcript, only the turn still in flight is shown from here.
			if sessionID > 0 && (c.Status == "done" || c.Status == "failed" || c.Status == "stopped" || c.Status == "cancelled") {
				continue
			}
			data.Turns = append(data.Turns, tv)
		}
	}
	if data.Busy && runID > 0 {
		if text, err := s.Store.RunLogTail(ctx, runID, 8<<10); err == nil {
			lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
			if len(text) == 8<<10 && len(lines) > 1 {
				lines = lines[1:] // the first line is cut off
			}
			data.LiveLog = strings.Join(lines[max(0, len(lines)-25):], "\n")
		}
	}
	if err := s.pages["agent"].ExecuteTemplate(&buf, "conversation", data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil //nolint:gosec // rendered by html/template
}

func (s *Server) agentConversation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	a, err := s.Store.AgentSession(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	var sid int64
	if a.ProviderSession != "" {
		sid, _ = s.Store.SessionByUUID(ctx, a.Host, a.ProviderSession)
	}
	run, _ := s.Store.LatestRunByKey(ctx, a.Host, "agent-"+strconv.FormatInt(a.ID, 10))
	html, err := s.conversation(ctx, a, sid, run)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Status", a.Status)
	_, _ = io.WriteString(w, string(html))
}

func (s *Server) agentSend(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := s.Agents.Ctl.FollowUp(r.Context(), id, r.PostFormValue("prompt"))
	s.back(w, r, idPath(id, "/agents/%d"), err)
}

func (s *Server) agentStop(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := idPath(id, "/agents/%d")
	if r.PostFormValue("from") == "agents" {
		to = "/agents"
	}
	s.back(w, r, to, s.Agents.Ctl.Stop(r.Context(), id))
}

func (s *Server) agentReview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	rid, err := s.Agents.Ctl.Review(r.Context(), id, strings.TrimSpace(r.PostFormValue("base")))
	if err != nil {
		s.back(w, r, idPath(id, "/agents/%d"), err)
		return
	}
	http.Redirect(w, r, idPath(rid, "/agents/%d"), http.StatusSeeOther)
}

func (s *Server) agentSendBack(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	orig, err := s.Agents.Ctl.SendBack(r.Context(), id)
	if err != nil {
		s.back(w, r, idPath(id, "/agents/%d"), err)
		return
	}
	http.Redirect(w, r, idPath(orig, "/agents/%d"), http.StatusSeeOther)
}

// ---------------------------------------------------------------- any ingested session

func (s *Server) sessionAgent(w http.ResponseWriter, r *http.Request) {
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
	data := struct {
		S         store.Session
		Live      bool
		Resumable bool
		Agent     *store.AgentSession
		CanSend   bool
		Other     string
		Resume    string
		Err       string
	}{S: sess, Live: s.Agents.Ctl.HostLive(ctx, sess.Host), Other: agent.ProviderName(agent.Other(sess.Provider)),
		Resume: agent.ResumeCommand(sess.Provider, sess.Cwd, sess.UUID), Err: r.URL.Query().Get("err")}
	data.Resumable = agent.KnownProvider(sess.Provider) && sess.UUID != "" && sess.ParentUUID == "" && sess.Cwd != ""
	if a, err := s.Store.AgentSessionByProvider(ctx, sess.Host, sess.Provider, sess.UUID); err == nil {
		data.Agent = &a
		data.CanSend = a.ReviewOf > 0 && !a.Busy() && strings.TrimSpace(a.LastResult) != ""
	}
	if !data.Resumable {
		data.Resume = ""
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "agent", "sessionctl", data)
}

func (s *Server) adopt(r *http.Request) (int64, int64, error) {
	id, ok := pathID(r)
	if !ok {
		return 0, 0, store.ErrNotFound
	}
	sess, err := s.Store.Session(r.Context(), id)
	if err != nil {
		return id, 0, err
	}
	aid, err := s.Agents.Ctl.Adopt(r.Context(), sess, r.PostFormValue("perm"), r.PostFormValue("bypass") == "1")
	return id, aid, err
}

func (s *Server) sessionSend(w http.ResponseWriter, r *http.Request) {
	id, aid, err := s.adopt(r)
	if err == nil {
		err = s.Agents.Ctl.FollowUp(r.Context(), aid, r.PostFormValue("prompt"))
	}
	if err != nil {
		if aid > 0 {
			s.back(w, r, idPath(aid, "/agents/%d"), err)
			return
		}
		s.back(w, r, idPath(id, "/sessions/%d")+"#agentctl", err)
		return
	}
	http.Redirect(w, r, idPath(aid, "/agents/%d"), http.StatusSeeOther)
}

func (s *Server) sessionReview(w http.ResponseWriter, r *http.Request) {
	id, aid, err := s.adopt(r)
	var rid int64
	if err == nil {
		rid, err = s.Agents.Ctl.Review(r.Context(), aid, strings.TrimSpace(r.PostFormValue("base")))
	}
	if err != nil {
		s.back(w, r, idPath(id, "/sessions/%d")+"#agentctl", err)
		return
	}
	http.Redirect(w, r, idPath(rid, "/agents/%d"), http.StatusSeeOther)
}

// ---------------------------------------------------------------- plain chats

type llmMsgView struct {
	store.LLMMessage
	HTML template.HTML
}

func (s *Server) llmPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok || s.Agents.Chats == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	c, err := s.Store.LLMChat(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	ms, err := s.Store.LLMMessages(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	var views []llmMsgView
	var streaming int64
	for _, m := range ms {
		v := llmMsgView{LLMMessage: m}
		if m.Role == "assistant" {
			v.HTML = llm.Markdown(m.Content)
			if m.Status == "streaming" {
				streaming = m.ID
				if text, _, _, ok := s.Agents.Chats.Snapshot(m.ID); ok {
					v.Content = text
				}
			}
		}
		views = append(views, v)
	}
	var groups []pickGroup
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	models, gwErr := s.Agents.Chats.C.Models(ctx2)
	cancel()
	for _, g := range llm.Grouped(models) {
		pg := pickGroup{Label: g.Vendor}
		for _, m := range g.Models {
			pg.Opts = append(pg.Opts, pickOpt{m.ID, m.ID})
		}
		groups = append(groups, pg)
	}
	known := false
	for _, m := range models {
		known = known || m.ID == c.Model
	}
	if !known && c.Model != "" {
		groups = append(groups, pickGroup{Label: "This chat", Opts: []pickOpt{{c.Model, c.Model}}})
	}
	errText := ""
	if gwErr != nil {
		errText = gwErr.Error()
	}
	s.render(w, "llmchat", "layout", struct {
		page
		C         store.LLMChat
		Msgs      []llmMsgView
		Streaming int64
		Groups    []pickGroup
		GwErr     string
		Err       string
	}{page{Title: c.Title, Nav: "chats"}, c, views, streaming, groups, errText, r.URL.Query().Get("err")})
}

func (s *Server) llmSend(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok || s.Agents.Chats == nil {
		http.NotFound(w, r)
		return
	}
	model := r.PostFormValue("model")
	_, err := s.Agents.Chats.Send(r.Context(), id, model, r.PostFormValue("prompt"))
	if err == nil {
		_ = s.Store.SetSettings(r.Context(), map[string]string{store.KeyChatLast: "plain:" + model})
	}
	s.back(w, r, idPath(id, "/llms/%d")+"#composer", err)
}

// llmEvents streams a reply as server-sent events: "delta" with the text so far, then "done" with the
// rendered HTML (or the error).
func (s *Server) llmEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok || s.Agents.Chats == nil {
		http.NotFound(w, r)
		return
	}
	mid, _ := strconv.ParseInt(r.URL.Query().Get("msg"), 10, 64)
	m, err := s.Store.LLMMessage(r.Context(), mid)
	if err != nil || m.ChatID != id || m.Role != "assistant" {
		http.NotFound(w, r)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		fl.Flush()
	}
	done := func(content, errMsg string) {
		send("done", map[string]string{"html": string(llm.Markdown(content)), "error": errMsg})
	}
	last := ""
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		text, fin, errMsg, live := s.Agents.Chats.Snapshot(mid)
		if !live {
			m, err := s.Store.LLMMessage(r.Context(), mid)
			if err != nil {
				return
			}
			if m.Status == "streaming" {
				m.Error = "interrupted"
			}
			done(m.Content, m.Error)
			return
		}
		if text != last {
			last = text
			send("delta", map[string]string{"text": text})
		}
		if fin {
			done(text, errMsg)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------- prompt library

func (s *Server) promptsPage(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Store.SavedPrompts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	var edit store.SavedPrompt
	if id, err := strconv.ParseInt(r.URL.Query().Get("edit"), 10, 64); err == nil {
		edit, _ = s.Store.SavedPrompt(r.Context(), id)
	}
	type row struct {
		store.SavedPrompt
		Vars []string
	}
	var rows []row
	for _, p := range ps {
		rows = append(rows, row{p, agent.PromptVars(p.Body)})
	}
	s.render(w, "prompts", "layout", struct {
		page
		Prompts []row
		Edit    store.SavedPrompt
		Err     string
	}{page{Title: "Prompt library", Nav: "chats"}, rows, edit, r.URL.Query().Get("err")})
}

func (s *Server) promptSave(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	p := store.SavedPrompt{ID: id, Name: strings.TrimSpace(r.PostFormValue("name")), Body: r.PostFormValue("body")}
	if p.Name == "" || strings.TrimSpace(p.Body) == "" || len(p.Name) > 120 || len(p.Body) > 256<<10 {
		s.back(w, r, "/prompts", agent.InputError{Msg: "a prompt needs a name (up to 120 characters) and text"})
		return
	}
	if _, err := s.Store.SavePrompt(r.Context(), p); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			err = agent.InputError{Msg: "there is already a prompt called " + p.Name}
		}
		s.back(w, r, "/prompts", err)
		return
	}
	s.Log.Printf("prompts: saved %q", p.Name)
	http.Redirect(w, r, "/prompts", http.StatusSeeOther)
}

func (s *Server) promptDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.back(w, r, "/prompts", s.Store.DeletePrompt(r.Context(), id))
}
