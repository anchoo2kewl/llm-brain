package memory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// TaskAI is the REST client for the knowledge base the memories live in (the same server the taskai MCP
// tools use). Auth is "Authorization: ApiKey <key>". Every memory is one wiki page in one project; TaskAI
// splits the page into blocks and indexes them (Postgres full text + pgvector, merged with RRF) a minute or
// two after a write.
type TaskAI struct {
	URL     string // e.g. https://taskai.cc (the API is under /api)
	Key     string
	Project string // the project memories go to, by name, found or made on first use ("Agent memory")
	// ProjectID, when set, skips the lookup.
	ProjectID int64
	HTTP      *http.Client

	mu sync.Mutex // ProjectID, found on first use
}

// KeyFromEnvFile reads TASKAI_API_KEY (or TASKAI_X_API_KEY, the name the MCP config export uses) from a
// KEY=value file. It returns "" when the file or the key is missing.
func KeyFromEnvFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export ")), "=")
		if ok {
			vals[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	for _, k := range []string{"TASKAI_API_KEY", "TASKAI_X_API_KEY"} {
		if vals[k] != "" {
			return vals[k]
		}
	}
	return ""
}

// APIError is a non-2xx answer.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string { return fmt.Sprintf("taskai: HTTP %d: %s", e.Status, e.Body) }

func (t *TaskAI) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(t.URL, "/")+"/api"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "ApiKey "+t.Key)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := t.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &APIError{Status: resp.StatusCode, Body: msg}
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

type project struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// EnsureProject finds the memory project by name, making it when it doesn't exist, and remembers its id.
func (t *TaskAI) EnsureProject(ctx context.Context) (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ProjectID > 0 {
		return t.ProjectID, nil
	}
	name := t.Project
	if name == "" {
		name = "Agent memory"
	}
	var ps []project
	if err := t.do(ctx, http.MethodGet, "/projects", nil, &ps); err != nil {
		return 0, err
	}
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) {
			t.ProjectID = p.ID
			return p.ID, nil
		}
	}
	var p project
	if err := t.do(ctx, http.MethodPost, "/projects", map[string]string{"name": name,
		"description": "Shared agent memory written by llm-brain: one page per lesson (fact, decision, gotcha, runbook, handoff)."}, &p); err != nil {
		return 0, err
	}
	if p.ID == 0 {
		return 0, fmt.Errorf("taskai: project %q made without an id", name)
	}
	t.ProjectID = p.ID
	return p.ID, nil
}

type page struct {
	ID int64 `json:"id"`
}

// CreatePage makes an empty page and returns its id.
func (t *TaskAI) CreatePage(ctx context.Context, projectID int64, title string) (int64, error) {
	var p page
	if err := t.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%d/wiki/pages", projectID), map[string]string{"title": title}, &p); err != nil {
		return 0, err
	}
	if p.ID == 0 {
		return 0, fmt.Errorf("taskai: page made without an id")
	}
	return p.ID, nil
}

// SetTitle renames a page.
func (t *TaskAI) SetTitle(ctx context.Context, pageID int64, title string) error {
	return t.do(ctx, http.MethodPatch, fmt.Sprintf("/wiki/pages/%d", pageID), map[string]string{"title": title}, nil)
}

// SetContent replaces a page's markdown.
func (t *TaskAI) SetContent(ctx context.Context, pageID int64, markdown string) error {
	return t.do(ctx, http.MethodPut, fmt.Sprintf("/wiki/pages/%d/content", pageID), map[string]any{"content": markdown, "manual_save": true}, nil)
}

// Hit is one block TaskAI's search returned.
type Hit struct {
	PageID       int64   `json:"page_id"`
	PageTitle    string  `json:"page_title"`
	BlockID      int64   `json:"block_id"`
	HeadingsPath string  `json:"headings_path,omitempty"`
	Snippet      string  `json:"snippet"`
	Rank         float64 `json:"rank,omitempty"`
}

// Search runs TaskAI's wiki search in the memory project. mode: hybrid | fts | semantic | keyword.
func (t *TaskAI) Search(ctx context.Context, query, mode string, limit, recencyDays int) ([]Hit, error) {
	pid, err := t.EnsureProject(ctx)
	if err != nil {
		return nil, err
	}
	req := map[string]any{"query": query, "project_id": pid, "limit": limit}
	if mode != "" {
		req["mode"] = mode
	}
	if recencyDays > 0 {
		req["recency_days"] = recencyDays
	}
	var out struct {
		Results []Hit `json:"results"`
	}
	if err := t.do(ctx, http.MethodPost, "/wiki/search", req, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}
