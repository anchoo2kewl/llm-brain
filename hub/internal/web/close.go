package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Closing a session from any page: a hub terminal is ended as a terminal, a hub task's running turn is
// stopped, and any other Claude/Codex/Gemini session (Terminal.app, an IDE, any Mac) gets a session_close
// that its Mac carries out (agent/close.go). Every Close asks once more before it acts, in the page itself.

func (s *Server) closeRoutes(g, p func(string, http.HandlerFunc)) {
	p("POST /sessions/{id}/close", s.sessionClose)
	p("POST /api/sessions/{id}/close", s.sessionCloseAPI)
	g("GET /api/sessions/{id}/close", s.sessionCloseState)
	p("POST /api/terminals/{id}/close", s.terminalCloseAPI)
}

// closableFor: how long after its last write a session still offers Close (an idle CLI can sit for days).
const closableFor = 72 * time.Hour

// closable: a session the hub can offer to close.
func closable(x store.Session) bool {
	return x.ParentUUID == "" && !x.Closed() && x.RunID == 0 && !strings.HasPrefix(x.AgentName, "fleet-") &&
		!agent.HeadlessOriginator(x.Originator) && (x.Provider == "claude" || x.Provider == "codex" || x.Provider == "gemini") &&
		x.FileMtime >= store.T(time.Now().Add(-closableFor))
}

// closing: a close is on its way to the Mac.
func closing(x store.Session) bool {
	return x.CloseCmd > 0 && !x.Closed() && (x.CloseStatus == "queued" || x.CloseStatus == "claimed" || x.CloseStatus == "running")
}

// claimTerminals maps session id → the open hub terminal that runs it. A terminal that knows its CLI's
// session id claims that session; otherwise it claims the first session to start in the same Mac, folder and
// CLI after it was opened. Every other session is somebody else's, even in the same folder.
func claimTerminals(ts []store.Terminal, ss []store.Session) map[int64]store.Terminal {
	mine := map[int64]store.Terminal{}
	taken := map[int64]bool{}
	for _, t := range ts {
		if !t.Open() || t.SessionUUID == "" {
			continue
		}
		for _, x := range ss {
			if x.Host == t.Host && x.Provider == t.Provider && x.UUID == t.SessionUUID {
				mine[x.ID], taken[t.ID] = t, true
				break
			}
		}
	}
	for _, t := range ts {
		if !t.Open() || taken[t.ID] {
			continue
		}
		var best *store.Session
		for i := range ss {
			x := &ss[i]
			if _, ok := mine[x.ID]; ok || x.ParentUUID != "" || x.Host != t.Host || x.Cwd != t.Cwd || x.Provider != t.Provider || x.FirstAt < t.CreatedAt {
				continue
			}
			if best == nil || x.FirstAt < best.FirstAt {
				best = x
			}
		}
		if best != nil {
			mine[best.ID] = t
		}
	}
	return mine
}

// terminalOf is the open hub terminal running session x, if any.
func (s *Server) terminalOf(ctx context.Context, x store.Session) (store.Terminal, bool) {
	if t, err := s.Store.SessionTerminal(ctx, x); err == nil && t.Open() {
		return t, true
	}
	ss, _ := s.Store.LiveSessions(ctx)
	found := false
	for _, y := range ss {
		found = found || y.ID == x.ID
	}
	if !found {
		ss = append(ss, x)
	}
	ts, _ := s.Store.Terminals(ctx, "", 200)
	t, ok := claimTerminals(ts, ss)[x.ID]
	return t, ok
}

// closeOut says what a Close did: ended a terminal, stopped a task, or queued a session_close.
type closeOut struct {
	Closing string `json:"closing"` // terminal | task | session
	ID      int64  `json:"id"`      // the terminal, task or command
	Status  string `json:"status"`  // ending | stopping | queued
}

func (s *Server) closeSession(ctx context.Context, x store.Session) (closeOut, error) {
	if t, ok := s.terminalOf(ctx, x); ok {
		return closeOut{"terminal", t.ID, "ending"}, s.Agents.Ctl.KillTerminal(ctx, t.ID)
	}
	if x.UUID != "" {
		if a, err := s.Store.AgentSessionByProvider(ctx, x.Host, x.Provider, x.UUID); err == nil {
			if !a.Busy() {
				return closeOut{}, agent.InputError{Msg: "this is a hub task and nothing is running; it has no process to close"}
			}
			return closeOut{"task", a.ID, "stopping"}, s.Agents.Ctl.Stop(ctx, a.ID)
		}
	}
	id, err := s.Agents.Ctl.CloseSession(ctx, x)
	return closeOut{"session", id, "queued"}, err
}

func (s *Server) sessionClose(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := backFrom(r, idPath(id, "/sessions/%d"))
	x, err := s.Store.Session(r.Context(), id)
	if err == nil {
		_, err = s.closeSession(r.Context(), x)
	}
	s.back(w, r, to, err)
}

func (s *Server) sessionCloseAPI(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	x, err := s.Store.Session(r.Context(), id)
	if err != nil {
		closeErr(w, err)
		return
	}
	out, err := s.closeSession(r.Context(), x)
	if err != nil {
		closeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// sessionCloseState: where the last Close of a session got to.
func (s *Server) sessionCloseState(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	x, err := s.Store.Session(r.Context(), id)
	if err != nil {
		closeErr(w, err)
		return
	}
	out := struct {
		Closed  bool               `json:"closed"`
		Status  string             `json:"status"` // the session_close command's, '' if none
		Error   string             `json:"error,omitempty"`
		Result  *agent.CloseResult `json:"result,omitempty"`
		Command int64              `json:"command,omitempty"`
	}{Closed: x.Closed(), Status: x.CloseStatus, Command: x.CloseCmd}
	if x.CloseCmd > 0 {
		if c, err := s.Store.Command(r.Context(), x.CloseCmd); err == nil {
			res, e := agent.ParseCloseResult(c.Result)
			out.Error = e
			if store.FinalStatus(c.Status) && e == "" {
				out.Result = &res
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) terminalCloseAPI(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.Agents.Ctl.KillTerminal(r.Context(), id); err != nil {
		closeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, closeOut{"terminal", id, "ending"})
}

// backFrom is where a Close form goes back to: its back field if it's a path on the hub, else def.
func backFrom(r *http.Request, def string) string {
	if b := r.PostFormValue("back"); localPath(b) {
		return b
	}
	return def
}

// closeArgs is what the "close" template (parts.html) needs: where to post, where to come back to, the
// button's words and a line about what it does.
type closeArgs struct{ Action, Back, Label, Note string }

func mkClose(action, back, label, note string) closeArgs { return closeArgs{action, back, label, note} }

func closeErr(w http.ResponseWriter, err error) {
	switch msg, ok := isInput(err); {
	case ok:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	default:
		jsonErr(w, http.StatusInternalServerError, err)
	}
}

// closeError is the reason the last Close of x failed, for its page.
func (s *Server) closeError(ctx context.Context, x store.Session) string {
	if x.CloseCmd == 0 || x.CloseStatus != "failed" {
		return ""
	}
	c, err := s.Store.Command(ctx, x.CloseCmd)
	if err != nil {
		return ""
	}
	_, e := agent.ParseCloseResult(c.Result)
	return e
}
