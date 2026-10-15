package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Renaming from any page: a terminal, a task, a model chat or a transcript, hub-started or not. Things that
// are the same conversation are renamed together (a terminal and the transcript it writes; a task and its
// transcript), and a Claude transcript gets the name in its own file (what /rename writes), so
// `claude --resume` and the CLI show it too. Nothing is typed into a running CLI.

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
			s.titleSession(ctx, x, name)
		} else if t.SessionUUID != "" && t.Open() {
			s.renameLater(t, name) // no transcript yet: Claude writes it with the first message
		}
	case "session":
		x, err := s.Store.Session(ctx, id)
		if err != nil {
			return err
		}
		if err := s.Store.RenameSession(ctx, id, name); err != nil {
			return err
		}
		s.titleSession(ctx, x, name)
		if t, err := s.Store.SessionTerminal(ctx, x); err == nil {
			_ = s.Store.RenameTerminal(ctx, t.ID, name)
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
				s.titleSession(ctx, x, name)
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

// titleSession names the Claude transcript on its Mac; best effort (the hub's own name is already saved).
func (s *Server) titleSession(ctx context.Context, x store.Session, name string) {
	if s.Agents == nil {
		return
	}
	if err := s.Agents.Ctl.TitleSession(ctx, x, name); err != nil && s.Log != nil {
		s.Log.Printf("rename session %d: %v", x.ID, err)
	}
}

// renameLater applies a terminal's name to its transcript once the transcript shows up (up to 30 minutes),
// unless the terminal was renamed again meanwhile.
func (s *Server) renameLater(t store.Terminal, name string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		for ctx.Err() == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Second):
			}
			cur, err := s.Store.Terminal(ctx, t.ID)
			if err != nil || cur.Title != name {
				return // renamed again (that rename takes over) or gone
			}
			if x, err := s.Store.TerminalSession(ctx, cur); err == nil {
				_ = s.Store.RenameSession(ctx, x.ID, name)
				s.titleSession(ctx, x, name)
				return
			}
		}
	}()
}
