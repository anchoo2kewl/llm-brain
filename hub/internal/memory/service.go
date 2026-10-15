package memory

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Service saves memories and answers context packs. The active memories are held in memory, so a context pack
// never waits on the database (one connection, shared with ingestion) or the network.
type Service struct {
	S      *store.Store
	TaskAI *TaskAI // nil: memories stay on the hub (sync_state 'off')
	Log    *log.Logger
	// RemoteBudget is how long a context pack waits for TaskAI's search before answering without it (the
	// answer is cached, so the next pack for the same scope has it). 0 = 150 ms.
	RemoteBudget time.Duration
	// RemoteMode is TaskAI's search mode for packs: hybrid (default) or fts.
	RemoteMode string

	mu     sync.RWMutex
	active []Memory // every active memory, oldest first
	loaded bool

	cache remoteCache
	now   func() time.Time
}

func (s *Service) logf(f string, a ...any) {
	if s.Log != nil {
		s.Log.Printf(f, a...)
	}
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) db() *sql.DB { return s.S.DB }

// Load reads the active memories into memory. Save and the context pack call it on first use.
func (s *Service) Load(ctx context.Context) error {
	ms, err := list(ctx, s.db(), `WHERE status = 'active' ORDER BY id`)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.active, s.loaded = ms, true
	s.mu.Unlock()
	return nil
}

func (s *Service) ensure(ctx context.Context) error {
	s.mu.RLock()
	ok := s.loaded
	s.mu.RUnlock()
	if ok {
		return nil
	}
	return s.Load(ctx)
}

// Active is a copy of the active memories (expired ones included; Memory.Active tells them apart).
func (s *Service) Active(ctx context.Context) ([]Memory, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Memory(nil), s.active...), nil
}

// Result says what a write did.
type Result struct {
	Memory     Memory `json:"memory"`
	Outcome    string `json:"outcome"` // created | duplicate | superseded
	Supersedes int64  `json:"supersedes,omitempty"`
	Similarity string `json:"similarity,omitempty"` // why a near-duplicate was superseded
}

// NearDuplicate is the word overlap (Jaccard) above which a new memory in the same scope replaces an old one.
const NearDuplicate = 0.7

// Save checks and stores a memory. An exact repeat in the same scope returns the one already kept; a
// near-duplicate in the same scope, a re-import of the same file, or an explicit Supersedes replaces the
// older memory instead of adding a second one.
func (s *Service) Save(ctx context.Context, w Write) (Result, error) {
	m, err := Normalize(w, s.clock())
	if err != nil {
		return Result{}, err
	}
	if err := s.ensure(ctx); err != nil {
		return Result{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res := Result{Outcome: "created"}
	renew := false
	for _, a := range s.active {
		if a.Hash != m.Hash {
			continue
		}
		if a.Active(m.CreatedAt) {
			return Result{Memory: a, Outcome: "duplicate"}, nil
		}
		if m.Supersedes == 0 { // written again after it expired: renew it
			m.Supersedes, res.Similarity, renew = a.ID, "the same, after it expired", true
		}
	}
	if m.Source == "import" && m.SourcePath != "" {
		// The file is unchanged but its import was replaced since (an agent wrote a better version): keep that.
		prev, err := list(ctx, s.db(), `WHERE host = ? AND source_path = ? AND hash = ? AND status = 'superseded' ORDER BY id DESC LIMIT 1`,
			m.Author.Host, m.SourcePath, m.Hash)
		if err != nil {
			return Result{}, err
		}
		if len(prev) > 0 {
			// …unless it was replaced by a later version of the same file, and the file went back to this one.
			if by, err := get(ctx, s.db(), prev[0].Superseded); err != nil || by.Source != "import" || by.SourcePath != m.SourcePath {
				return Result{Memory: prev[0], Outcome: "duplicate"}, nil
			}
		}
	}
	switch {
	case renew:
	case m.Supersedes > 0:
		old, err := get(ctx, s.db(), m.Supersedes)
		if err != nil {
			return Result{}, InputError(fmt.Sprintf("supersedes: no memory %d", m.Supersedes))
		}
		if old.Status != "active" {
			return Result{}, InputError(fmt.Sprintf("supersedes: memory %d was already superseded by %d", old.ID, old.Superseded))
		}
	default:
		if old, why := s.replaces(m); old > 0 {
			m.Supersedes, res.Similarity = old, why
		}
	}
	if m.Supersedes > 0 {
		res.Outcome, res.Supersedes = "superseded", m.Supersedes
	}
	m.SyncState = "pending"
	if s.TaskAI == nil {
		m.SyncState = "off"
	}
	if err := insert(ctx, s.db(), &m); err != nil {
		return Result{}, err
	}
	kept := s.active[:0]
	for _, a := range s.active {
		if a.ID != m.Supersedes {
			kept = append(kept, a)
		}
	}
	s.active = append(kept, m)
	s.cache.clear()
	res.Memory = m
	return res, nil
}

// replaces finds the active memory m should replace: the same imported file, else the most similar one in the
// same scope. Call with s.mu held.
func (s *Service) replaces(m Memory) (int64, string) {
	if m.Source == "import" && m.SourcePath != "" {
		// An imported file only ever replaces its own earlier import: two files are two memories (else
		// each re-import would swap which of two similar files is kept).
		for _, a := range s.active {
			if a.Source == "import" && a.SourcePath == m.SourcePath && a.Author.Host == m.Author.Host {
				return a.ID, "same file, edited"
			}
		}
		return 0, ""
	}
	words := wordSet(m.Title + " " + m.Body)
	var best int64
	var bestSim float64
	for _, a := range s.active {
		if !sameScope(a.Scope, m.Scope) {
			continue
		}
		if sim := jaccard(words, wordSet(a.Title+" "+a.Body)); sim >= NearDuplicate && sim > bestSim {
			best, bestSim = a.ID, sim
		}
	}
	if best == 0 {
		return 0, ""
	}
	return best, fmt.Sprintf("%.0f%% the same words", bestSim*100)
}

// sameScope: the same repo (and issue/PR when either names one), or the same folder when neither has a repo.
func sameScope(a, b Scope) bool {
	if a.Repo != "" || b.Repo != "" {
		return strings.EqualFold(a.Repo, b.Repo) && a.Issue == b.Issue && a.PR == b.PR
	}
	return a.Folder == b.Folder
}

var wordRx = regexp.MustCompile(`[\p{L}\p{N}_]+`)

func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordRx.FindAllString(strings.ToLower(s), -1) {
		if len(w) > 1 {
			out[w] = true
		}
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return float64(n) / float64(len(a)+len(b)-n)
}

// Recent lists the newest memories, superseded ones included, for the /memory page.
func (s *Service) Recent(ctx context.Context, limit int) ([]Memory, error) {
	return list(ctx, s.db(), `ORDER BY id DESC LIMIT ?`, limit)
}

// Get returns one memory.
func (s *Service) Get(ctx context.Context, id int64) (Memory, error) { return get(ctx, s.db(), id) }

// Counts is how many memories are in each sync state, for the /memory page.
type Counts struct {
	Active, Superseded, Pending, Synced, Failing int
}

func (s *Service) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.db().QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'superseded' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN sync_state = 'pending' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN sync_state = 'synced' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN sync_state = 'pending' AND attempts > 0 THEN 1 ELSE 0 END), 0)
		FROM memories`).Scan(&c.Active, &c.Superseded, &c.Pending, &c.Synced, &c.Failing)
	return c, err
}
