package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Renaming from any page: a terminal, a task, a model chat or a transcript. Things that are the same
// conversation are renamed together (a terminal and the transcript it writes; a task and its transcript),
// and a Claude terminal also gets `/rename <name>` so the CLI's own session name matches.

func (s *Server) renameRoutes(p func(string, http.HandlerFunc)) {
	p("POST /sessions/{id}/rename", s.renameHandler("session"))
	p("POST /agents/{id}/rename", s.renameHandler("task"))
	p("POST /llms/{id}/rename", s.renameHandler("chat"))
}

// cleanName: one line, no control characters, at most 120 characters.
func cleanName(n string) (string, error) {
	n = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, n)
	n = strings.Join(strings.Fields(n), " ")
	if n == "" {
		return "", errors.New("a name can't be empty")
	}
	if len([]rune(n)) > 120 {
		return "", errors.New("keep the name under 120 characters")
	}
	return n, nil
}

func (s *Server) renameHandler(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		name, err := cleanName(r.FormValue("title"))
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err)
			return
		}
		if err := s.rename(r.Context(), kind, id, name); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				jsonErr(w, http.StatusNotFound, err)
				return
			}
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"title": name})
	}
}

// rename names kind/id and everything that is the same conversation.
func (s *Server) rename(ctx context.Context, kind string, id int64, name string) error {
	switch kind {
	case "terminal":
		t, err := s.Store.Terminal(ctx, id)
		if err != nil {
			return err
		}
		if err := s.Store.RenameTerminal(ctx, id, name); err != nil {
			return err
		}
		if x, err := s.Store.TerminalSession(ctx, t); err == nil {
			_ = s.Store.RenameSession(ctx, x.ID, name)
		}
		s.claudeRename(t, name)
	case "session":
		x, err := s.Store.Session(ctx, id)
		if err != nil {
			return err
		}
		if err := s.Store.RenameSession(ctx, id, name); err != nil {
			return err
		}
		if t, err := s.Store.SessionTerminal(ctx, x); err == nil {
			_ = s.Store.RenameTerminal(ctx, t.ID, name)
			s.claudeRename(t, name)
		}
		if a, err := s.Store.AgentSessionByProvider(ctx, x.Host, x.Provider, x.UUID); err == nil && x.UUID != "" {
			_ = s.Store.RenameAgentSession(ctx, a.ID, name)
		}
	case "task":
		a, err := s.Store.AgentSession(ctx, id)
		if err != nil {
			return err
		}
		if err := s.Store.RenameAgentSession(ctx, id, name); err != nil {
			return err
		}
		if a.ProviderSession != "" {
			if x, err := s.Store.SessionByProviderUUID(ctx, a.Host, a.Provider, a.ProviderSession); err == nil {
				_ = s.Store.RenameSession(ctx, x.ID, name)
			}
		}
	case "chat":
		if _, err := s.Store.LLMChat(ctx, id); err != nil {
			return err
		}
		return s.Store.RenameLLMChat(ctx, id, name)
	}
	return nil
}

// claudeRename types `/rename <name>` into a live Claude terminal once it is idle with an empty prompt, so
// it never lands in the middle of a turn or of something the person is typing. It tries for 10 minutes.
func (s *Server) claudeRename(t store.Terminal, name string) {
	if t.Provider != "claude" || !t.Open() || s.Agents == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		for ctx.Err() == nil {
			rep, err := s.termCommand(ctx, t.ID, agent.KindTermCapture, agent.TermCapture{})
			if err == nil && claudeIdle(rep.Result) {
				if _, err := s.termCommand(ctx, t.ID, agent.KindTermSend, agent.TermSend{Text: "/rename " + name, Keys: []string{"Enter"}}); err == nil {
					return
				}
			}
			if err != nil && strings.Contains(err.Error(), "the terminal is") {
				return // ended
			}
			select {
			case <-ctx.Done():
			case <-time.After(15 * time.Second):
			}
		}
	}()
}

// claudeIdle: Claude Code's screen shows its input box with nothing typed and no turn running.
func claudeIdle(screen string) bool {
	if strings.Contains(screen, "esc to interrupt") {
		return false
	}
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-20; i-- {
		l := strings.TrimSpace(lines[i])
		if rest, ok := strings.CutPrefix(l, "❯"); ok {
			rest = strings.TrimSpace(rest)
			return rest == "" || strings.HasPrefix(rest, `Try "`) // the placeholder, not typed text
		}
	}
	return false
}
