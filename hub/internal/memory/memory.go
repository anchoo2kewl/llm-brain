// Package memory is the shared agent memory: small, verbatim lessons (a fact, a decision, a gotcha, a
// runbook, a handoff) that any agent writes as it learns and every agent reads before it works.
//
// The hub is the write path. A memory is checked (kind, scope, size, no secrets), compared with what is
// already known (an exact repeat is a no-op; a near-duplicate supersedes the older one), kept in the hub's
// store, and forwarded to TaskAI as one wiki page per memory in an "Agent memory" project, retried until it
// lands (sync.go). TaskAI indexes it for hybrid search, so people and agents can find it there too.
//
// The read path is the context pack (pack.go): the top memories for a repo, issue, PR, folder and files,
// scored in memory on the hub (fast, no network), merged with TaskAI's own search within a time budget,
// plus who else is working on the same thing right now (presence.go).
//
// Layout on TaskAI: one project for every repo, one page per memory, with the repo, issue, PR and files written
// as words in the title and on a scope line. TaskAI's search filters only by project, so per-repo projects
// would mean making a project per repo for no gain. The scope filter that matters runs on the hub, which holds
// every memory with its scope as columns; TaskAI's ranking is merged in on top. The hub is the source of truth:
// a memory exists once it is on the hub, and TaskAI is a copy people can read and search.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Kinds of memory.
const (
	KindFact     = "fact"
	KindDecision = "decision"
	KindGotcha   = "gotcha"
	KindRunbook  = "runbook"
	KindHandoff  = "handoff"
)

var Kinds = []string{KindFact, KindDecision, KindGotcha, KindRunbook, KindHandoff}

// Size limits: a memory is a lesson, not a transcript.
const (
	MaxTitle = 140
	MaxBody  = 2000
	MaxFiles = 20
)

// Memory is one lesson with its scope and author.
type Memory struct {
	ID         int64     `json:"id"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Scope      Scope     `json:"scope"`
	Author     Author    `json:"author"`
	Confidence string    `json:"confidence"` // low | medium | high
	Supersedes int64     `json:"supersedes,omitempty"`
	Superseded int64     `json:"superseded_by,omitempty"`
	Status     string    `json:"status"` // active | superseded
	ExpiresAt  time.Time `json:"expires_at,omitzero"`
	Hash       string    `json:"-"`
	Source     string    `json:"source,omitempty"` // api | import
	SourcePath string    `json:"source_path,omitempty"`
	PageID     int64     `json:"page_id,omitempty"`
	SyncState  string    `json:"sync_state,omitempty"` // pending | synced | off
	Attempts   int       `json:"-"`
	NextTry    time.Time `json:"-"`
	LastError  string    `json:"last_error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Scope says what a memory is about. At least one of Repo, Folder is set; Issue and PR need Repo.
type Scope struct {
	Repo   string   `json:"repo,omitempty"` // owner/name
	Issue  int      `json:"issue,omitempty"`
	PR     int      `json:"pr,omitempty"`
	Folder string   `json:"folder,omitempty"` // absolute path on the author's host
	Files  []string `json:"files,omitempty"`  // repo-relative
}

// Author is who wrote it.
type Author struct {
	Agent    string `json:"agent,omitempty"`    // claude-code, codex, fleet-issue-12, a person's name …
	Provider string `json:"provider,omitempty"` // claude | codex | gemini | human
	Host     string `json:"host,omitempty"`
	Session  string `json:"session,omitempty"` // the CLI session id
}

// Active: not superseded and not expired.
func (m Memory) Active(now time.Time) bool {
	return m.Status == "active" && (m.ExpiresAt.IsZero() || now.Before(m.ExpiresAt))
}

// Write is a request to save a memory (POST /api/memory).
type Write struct {
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	Scope      Scope  `json:"scope"`
	Author     Author `json:"author"`
	Confidence string `json:"confidence,omitempty"`
	Supersedes int64  `json:"supersedes,omitempty"`
	// ExpiresDays: drop it from context packs after this many days; 0 = the kind's default (handoff 14,
	// fact 180, the rest never); -1 = never.
	ExpiresDays int    `json:"expires_days,omitempty"`
	Source      string `json:"source,omitempty"`      // import (hub memory import) or empty
	SourcePath  string `json:"source_path,omitempty"` // import: the file on Author.Host
}

// InputError is a write the hub refuses; the message is safe to show the writer.
type InputError string

func (e InputError) Error() string { return string(e) }

var (
	repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	wsRe   = regexp.MustCompile(`[ \t]+`)
)

// Normalize checks w and returns the memory it describes (not yet saved).
func Normalize(w Write, now time.Time) (Memory, error) {
	m := Memory{
		Kind:       strings.ToLower(strings.TrimSpace(w.Kind)),
		Title:      strings.TrimSpace(wsRe.ReplaceAllString(w.Title, " ")),
		Body:       strings.TrimSpace(w.Body),
		Scope:      w.Scope,
		Author:     w.Author,
		Confidence: strings.ToLower(strings.TrimSpace(w.Confidence)),
		Supersedes: w.Supersedes,
		Status:     "active",
		Source:     w.Source,
		SourcePath: w.SourcePath,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	ok := false
	for _, k := range Kinds {
		ok = ok || m.Kind == k
	}
	if !ok {
		return m, InputError("kind must be one of " + strings.Join(Kinds, ", "))
	}
	if m.Title == "" {
		// A one-line memory may be all body: its first line is the title.
		m.Title, _, _ = strings.Cut(m.Body, "\n")
		m.Title = strings.TrimSpace(m.Title)
	}
	if strings.ContainsAny(m.Title, "\n\r") {
		return m, InputError("title must be one line")
	}
	if m.Title == "" || m.Body == "" {
		return m, InputError("a memory needs a body")
	}
	if len(m.Title) > MaxTitle {
		return m, InputError(fmt.Sprintf("title is %d bytes; keep it under %d", len(m.Title), MaxTitle))
	}
	if len(m.Body) > MaxBody {
		return m, InputError(fmt.Sprintf("body is %d bytes; a memory is a lesson, not a transcript: keep it under %d", len(m.Body), MaxBody))
	}
	switch m.Confidence {
	case "":
		m.Confidence = "medium"
	case "low", "medium", "high":
	default:
		return m, InputError("confidence must be low, medium or high")
	}
	sc := &m.Scope
	sc.Repo = strings.TrimSuffix(strings.TrimSpace(sc.Repo), ".git")
	sc.Folder = strings.TrimRight(strings.TrimSpace(sc.Folder), "/")
	if sc.Repo != "" && !repoRe.MatchString(sc.Repo) {
		return m, InputError("scope.repo must be owner/name")
	}
	if sc.Folder != "" && !strings.HasPrefix(sc.Folder, "/") {
		return m, InputError("scope.folder must be an absolute path")
	}
	if sc.Repo == "" && sc.Folder == "" {
		return m, InputError("scope needs a repo or a folder")
	}
	if (sc.Issue != 0 || sc.PR != 0) && sc.Repo == "" {
		return m, InputError("scope.issue and scope.pr need scope.repo")
	}
	if sc.Issue < 0 || sc.PR < 0 {
		return m, InputError("scope.issue and scope.pr are positive numbers")
	}
	if len(sc.Files) > MaxFiles {
		return m, InputError(fmt.Sprintf("at most %d files", MaxFiles))
	}
	var files []string
	for _, f := range sc.Files {
		if f = strings.TrimPrefix(strings.TrimSpace(f), "./"); f != "" {
			files = append(files, f)
		}
	}
	sc.Files = files
	if m.Author.Host == "" && m.Author.Agent == "" {
		return m, InputError("author needs an agent or a host")
	}
	if m.Source != "" && m.Source != "import" && m.Source != "api" {
		return m, InputError("source must be import or empty")
	}
	if m.Source == "" {
		m.Source = "api"
	}
	if f := Secret(m.Title, m.Body, sc.Repo, sc.Folder, strings.Join(sc.Files, " "), m.SourcePath); f != "" {
		return m, InputError("refused: it looks like it holds a credential (" + f + "); memories must never store secrets")
	}
	days := w.ExpiresDays
	if days == 0 {
		days = map[string]int{KindHandoff: 14, KindFact: 180}[m.Kind]
	}
	if days > 0 {
		m.ExpiresAt = now.Add(time.Duration(days) * 24 * time.Hour)
	}
	m.Hash = hashOf(m)
	return m, nil
}

// hashOf identifies a memory's content and scope, ignoring case and spacing.
func hashOf(m Memory) string {
	h := sha256.New()
	for _, s := range []string{m.Kind, norm(m.Title), norm(m.Body), strings.ToLower(m.Scope.Repo), fmt.Sprint(m.Scope.Issue),
		fmt.Sprint(m.Scope.PR), m.Scope.Folder, strings.Join(m.Scope.Files, ",")} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func norm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// RepoName is the name part of owner/name.
func RepoName(repo string) string {
	if _, n, ok := strings.Cut(repo, "/"); ok {
		return n
	}
	return repo
}
