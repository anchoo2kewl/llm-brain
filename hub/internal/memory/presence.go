package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Presence: who is working on what right now, from what the hub already knows: live transcripts (any
// Claude/Codex session on any Mac), running fleet workers, and open hub terminals.

// Source is the part of the store presence reads (*store.Store).
type Source interface {
	LiveSessions(ctx context.Context) ([]store.Session, error)
	RunningRuns(ctx context.Context) ([]store.Run, error)
	Terminals(ctx context.Context, q string, limit int) ([]store.Terminal, error)
}

// Presence is one agent at work.
type Presence struct {
	Kind     string    `json:"kind"` // session | run | terminal
	Host     string    `json:"host"`
	Provider string    `json:"provider,omitempty"`
	Folder   string    `json:"folder,omitempty"`
	Branch   string    `json:"branch,omitempty"`
	Repo     string    `json:"repo,omitempty"` // owner/name when known (fleet runs), else the folder's repo name
	Issue    int       `json:"issue,omitempty"`
	PR       int       `json:"pr,omitempty"`
	Title    string    `json:"title,omitempty"`
	Active   time.Time `json:"last_active"`
	Link     string    `json:"link"` // the hub page
	Why      string    `json:"why,omitempty"`
}

// Want is what a presence query is about; empty matches everyone.
type Want struct {
	Repo   string // owner/name or name
	Issue  int
	PR     int
	Folder string
	// Self is the asker's own session id (Claude/Codex uuid), left out of the answer.
	Self string
}

func (w Want) empty() bool { return w.Repo == "" && w.Issue == 0 && w.PR == 0 && w.Folder == "" }

var (
	targetRe = regexp.MustCompile(`github\.com/([^/\s]+/[^/\s]+)/(issues|pull)/(\d+)`)
	// issue numbers in branch names: 17446-fix, issue-17446, fix/17446-x, gh-12, agent/issue-12
	branchNumRe = regexp.MustCompile(`(?:^|[/_-])(?:issue[-_]?|gh[-_]?|#)?(\d{1,7})(?:$|[/_-])`)
	hashNumRe   = regexp.MustCompile(`#(\d{1,7})\b`)
)

// Who lists the agents at work, the ones relevant to w first (all of them when w is empty).
func Who(ctx context.Context, src Source, w Want) ([]Presence, error) {
	var all []Presence
	ss, err := src.LiveSessions(ctx)
	if err != nil {
		return nil, err
	}
	for _, x := range ss {
		if w.Self != "" && x.UUID == w.Self {
			continue
		}
		p := Presence{Kind: "session", Host: x.Host, Provider: x.Provider, Folder: x.Cwd, Branch: x.Branch, Title: x.DisplayTitle(),
			Active: store.ParseT(x.FileMtime), Link: fmt.Sprintf("/sessions/%d", x.ID)}
		if x.RunID > 0 {
			continue // a fleet worker's own transcript: the run says more
		}
		p.Repo = repoOfFolder(x.Cwd)
		p.Issue = numIn(x.Branch, x.Title)
		all = append(all, p)
	}
	rs, err := src.RunningRuns(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if !r.HostLive {
			continue
		}
		p := Presence{Kind: "run", Host: r.Host, Provider: r.Provider, Title: r.Key, Active: store.ParseT(r.StartedAt),
			Link: fmt.Sprintf("/runs/%d", r.ID)}
		if m := targetRe.FindStringSubmatch(r.Target); m != nil {
			p.Repo = m[1]
			n, _ := strconv.Atoi(m[3])
			if m[2] == "pull" {
				p.PR = n
			} else {
				p.Issue = n
			}
		}
		all = append(all, p)
	}
	ts, err := src.Terminals(ctx, "", 200)
	if err != nil {
		return nil, err
	}
	for _, t := range ts {
		if !t.Open() {
			continue
		}
		if w.Self != "" && t.SessionUUID == w.Self {
			continue
		}
		all = append(all, Presence{Kind: "terminal", Host: t.Host, Provider: t.Provider, Folder: t.Cwd, Title: t.Title,
			Repo: repoOfFolder(t.Cwd), Issue: numIn("", t.Title), Active: store.ParseT(t.UpdatedAt), Link: fmt.Sprintf("/terminals/%d", t.ID)})
	}
	if w.empty() {
		sort.SliceStable(all, func(i, j int) bool { return all[i].Active.After(all[j].Active) })
		return all, nil
	}
	var out []Presence
	for _, p := range all {
		if why := relevance(p, w); why != "" {
			p.Why = why
			out = append(out, p)
		}
	}
	rank := map[string]int{"same issue": 0, "same PR": 0, "same folder": 1, "same repo": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := rank[out[i].Why], rank[out[j].Why]; a != b {
			return a < b
		}
		return out[i].Active.After(out[j].Active)
	})
	return out, nil
}

// repoOfFolder is the repo name a folder holds: the folder's name, without a -wt-<name> worktree suffix.
func repoOfFolder(dir string) string {
	if dir == "" {
		return ""
	}
	b := filepath.Base(dir)
	if i := strings.Index(b, "-wt-"); i > 0 {
		b = b[:i]
	}
	return b
}

func numIn(branch, title string) int {
	if m := branchNumRe.FindStringSubmatch(branch); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if m := hashNumRe.FindStringSubmatch(title); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// sameRepo compares owner/name with owner/name, or just names when either side only knows the folder's name.
func sameRepo(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if strings.Contains(a, "/") && strings.Contains(b, "/") {
		return strings.EqualFold(a, b)
	}
	return strings.EqualFold(RepoName(a), RepoName(b))
}

func underOrSame(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = strings.TrimRight(a, "/"), strings.TrimRight(b, "/")
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func relevance(p Presence, w Want) string {
	repo := sameRepo(p.Repo, w.Repo)
	switch {
	case w.Issue > 0 && p.Issue == w.Issue && (repo || w.Repo == ""):
		return "same issue"
	case w.PR > 0 && p.PR == w.PR && (repo || w.Repo == ""):
		return "same PR"
	case underOrSame(p.Folder, w.Folder):
		return "same folder"
	case repo:
		return "same repo"
	}
	return ""
}
