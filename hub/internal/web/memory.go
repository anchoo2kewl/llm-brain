package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/anchoo2kewl/llm-brain/hub/internal/memory"
)

// Shared agent memory (package memory): agents write lessons to POST /api/memory and read a context pack
// before they work; /memory shows what is kept and who is working on what. Tailnet only, like the rest.

func (s *Server) memoryRoutes(mux *http.ServeMux) {
	if s.Memory == nil {
		return
	}
	mux.HandleFunc("POST /api/memory", s.tailnetOnly(s.sameSite(s.memoryWrite)))
	mux.HandleFunc("GET /api/memory", s.tailnetOnly(s.memoryList))
	mux.HandleFunc("GET /api/memory/context", s.tailnetOnly(s.memoryContext))
	mux.HandleFunc("GET /api/presence", s.tailnetOnly(s.presenceAPI))
	mux.HandleFunc("GET /memory", s.tailnetOnly(s.memoryPage))
}

func (s *Server) memoryWrite(w http.ResponseWriter, r *http.Request) {
	var req memory.Write
	if !jsonBody(w, r, &req) {
		return
	}
	res, err := s.Memory.Save(r.Context(), req)
	var ie memory.InputError
	switch {
	case errors.As(err, &ie):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": ie.Error()})
	case err != nil:
		s.Log.Printf("memory: save: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	case res.Outcome == "duplicate":
		writeJSON(w, http.StatusOK, res)
	default:
		writeJSON(w, http.StatusCreated, res)
	}
}

func (s *Server) memoryList(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 500 {
		n = 100
	}
	ms, err := s.Memory.Recent(r.Context(), n)
	if err != nil {
		s.fail(w, err)
		return
	}
	if ms == nil {
		ms = []memory.Memory{}
	}
	writeJSON(w, http.StatusOK, ms)
}

// packRequest reads repo, issue, pr, folder (or cwd), files (comma-separated or repeated), q, k and self.
func packRequest(r *http.Request) memory.Request {
	q := r.URL.Query()
	atoi := func(k string) int { n, _ := strconv.Atoi(strings.TrimPrefix(q.Get(k), "#")); return max(n, 0) }
	var files []string
	for _, v := range q["files"] {
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				files = append(files, f)
			}
		}
	}
	folder := q.Get("folder")
	if folder == "" {
		folder = q.Get("cwd")
	}
	return memory.Request{Repo: q.Get("repo"), Issue: atoi("issue"), PR: atoi("pr"), Folder: folder, Files: files,
		Query: q.Get("q"), K: atoi("k"), Self: q.Get("self")}
}

func (s *Server) memoryContext(w http.ResponseWriter, r *http.Request) {
	req := packRequest(r)
	pk, err := s.Memory.Context(r.Context(), s.Store, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	if r.URL.Query().Get("format") == "md" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(memory.Markdown(pk, req)))
		return
	}
	writeJSON(w, http.StatusOK, pk)
}

func (s *Server) presenceAPI(w http.ResponseWriter, r *http.Request) {
	req := packRequest(r)
	ps, err := memory.Who(r.Context(), s.Store, memory.Want{Repo: req.Repo, Issue: req.Issue, PR: req.PR, Folder: req.Folder, Self: req.Self})
	if err != nil {
		s.fail(w, err)
		return
	}
	if ps == nil {
		ps = []memory.Presence{}
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) memoryPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ms, err := s.Memory.Recent(ctx, 200)
	if err != nil {
		s.fail(w, err)
		return
	}
	counts, err := s.Memory.Counts(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	who, err := memory.Who(ctx, s.Store, memory.Want{})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "memory", "layout", struct {
		page
		Memories []memory.Memory
		Counts   memory.Counts
		Presence []memory.Presence
		Synced   bool
	}{page{Title: "Memory", Nav: "memory"}, ms, counts, who, s.Memory.TaskAI != nil})
}
