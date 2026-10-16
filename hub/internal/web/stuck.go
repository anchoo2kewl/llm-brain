package web

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Frozen terminals: the host agent's watchdog (agent/stuck.go) finds a terminal whose CLI sits in a
// file-system call nobody can finish from here — a macOS privacy prompt on the Mac's own screen. The hub
// shows it as blocked everywhere, puts it in the inbox (and so in a push), says how to unstick it, and can
// restart the CLI in the same tmux, continuing its conversation.

// stuckView is what the "stuck" partial shows for one terminal.
type stuckView struct {
	ID    int64
	Host  string
	Block *store.TermBlock
	Back  string // where Restart returns to
}

func mkStuck(t store.Terminal, back string) stuckView {
	return stuckView{ID: t.ID, Host: t.Host, Block: t.Block(), Back: back}
}

// vncURL is Screen Sharing's link to a Mac (html/template refuses vnc: in a plain string).
func vncURL(host string) template.URL {
	return template.URL("vnc://" + url.PathEscape(host))
}

// stuckTitle and stuckDetail word a blocked terminal for the inbox and push.
func stuckTitle(t store.Terminal) string {
	return "Terminal " + itoa(t.ID) + " is waiting on a macOS permission prompt on " + t.Host
}

func stuckDetail(t store.Terminal, b *store.TermBlock) string {
	var sb strings.Builder
	sb.WriteString(orUntitled(t.Title))
	sb.WriteString(": ")
	if b.Proc != "" {
		sb.WriteString(b.Proc)
	} else {
		sb.WriteString("its CLI")
	}
	sb.WriteString(" is stuck in " + firstNonEmpty(b.Func, "a file call"))
	if b.Path != "" {
		sb.WriteString(" in " + b.Path)
	}
	sb.WriteString(". Click Allow on " + t.Host + "'s screen (Screen Sharing: vnc://" + t.Host + "), or give ")
	if len(b.FDA) > 0 {
		sb.WriteString(strings.Join(b.FDA, " and "))
	} else {
		sb.WriteString("the hub and tmux")
	}
	sb.WriteString(" Full Disk Access in System Settings → Privacy & Security → Full Disk Access, or restart the CLI.")
	return sb.String()
}

// restartResume is the conversation a restarted terminal continues: Claude's own session id once its
// transcript exists, otherwise the session the terminal claims (Codex). Empty starts afresh.
func (s *Server) restartResume(ctx context.Context, t store.Terminal) string {
	if t.Provider == "claude" && t.SessionUUID != "" {
		if x, err := s.Store.TerminalSession(ctx, t); err == nil {
			return x.UUID
		}
		return ""
	}
	if t.Provider != "codex" {
		return ""
	}
	sid := s.terminalSessions(ctx, []store.Terminal{t})[t.ID]
	if sid == 0 {
		return ""
	}
	if x, err := s.Store.Session(ctx, sid); err == nil && x.Provider == t.Provider {
		return x.UUID
	}
	return ""
}

func (s *Server) restartTerminal(ctx context.Context, id int64) (int64, error) {
	t, err := s.Store.Terminal(ctx, id)
	if err != nil {
		return 0, err
	}
	return s.Agents.Ctl.RestartTerminal(ctx, id, s.restartResume(ctx, t))
}

// terminalRestart: POST /terminals/{id}/restart (a form; back to where it came from).
func (s *Server) terminalRestart(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, err := s.restartTerminal(r.Context(), id)
	s.back(w, r, backFrom(r, idPath(id, "/terminals/%d")), err)
}

// terminalRestartAPI: POST /api/terminals/{id}/restart, for the workspace and scripts.
func (s *Server) terminalRestartAPI(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	cid, err := s.restartTerminal(r.Context(), id)
	if err != nil {
		if msg, ok := isInput(err); ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
			return
		}
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"command": cid})
}
