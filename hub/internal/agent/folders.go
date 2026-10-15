package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Roots are the folders sessions may run under. Every path is checked after resolving symlinks, so a link
// inside a root that points outside it is refused.
type Roots struct {
	real []string
}

func NewRoots(paths []string) Roots {
	var r Roots
	seen := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(expandHome(p))
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		rp, err := filepath.EvalSymlinks(filepath.Clean(p))
		if err != nil || seen[rp] {
			continue
		}
		if st, err := os.Stat(rp); err != nil || !st.IsDir() {
			continue
		}
		seen[rp] = true
		r.real = append(r.real, rp)
	}
	return r
}

// DefaultRoots: the fleet config's work_root when it exists, else ~/projects.
func DefaultRoots(c Config) []string {
	if c.WorkRoot != "" {
		if st, err := os.Stat(c.WorkRoot); err == nil && st.IsDir() {
			return []string{c.WorkRoot}
		}
	}
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(home, "projects")}
}

func (r Roots) Paths() []string { return append([]string(nil), r.real...) }

var (
	ErrOutside   = errors.New("folder is not under the allowed roots")
	ErrTraversal = errors.New("folder path must be absolute, without '..'")
)

// Validate returns the real path of dir when it is an existing directory strictly inside one of the roots.
func (r Roots) Validate(dir string) (string, error) {
	if dir == "" || !filepath.IsAbs(dir) || strings.ContainsRune(dir, 0) {
		return "", ErrTraversal
	}
	for _, el := range strings.Split(filepath.ToSlash(dir), "/") {
		if el == ".." {
			return "", ErrTraversal
		}
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return "", fmt.Errorf("folder %s: %w", dir, os.ErrNotExist)
	}
	st, err := os.Stat(real)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	if !r.inside(real) {
		return "", ErrOutside
	}
	return real, nil
}

func (r Roots) inside(real string) bool {
	for _, root := range r.real {
		rel, err := filepath.Rel(root, real)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// Scan lists git repos and worktrees one or two levels under each root (root/repo, root/group/repo).
func (r Roots) Scan(limit int) []store.AgentFolder {
	var out []store.AgentFolder
	seen := map[string]bool{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			if len(out) >= limit {
				return
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
				continue
			}
			p := filepath.Join(dir, name)
			if real, err := filepath.EvalSymlinks(p); err != nil || !r.inside(real) {
				continue
			}
			if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
				if !seen[p] {
					seen[p] = true
					out = append(out, store.AgentFolder{Path: p, Branch: gitBranch(p)})
				}
				continue
			}
			if depth < 2 {
				walk(p, depth+1)
			}
		}
	}
	for _, root := range r.real {
		walk(root, 1)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// gitBranch reads HEAD without running git (a worktree's .git is a file pointing at its gitdir).
func gitBranch(repo string) string {
	gd := filepath.Join(repo, ".git")
	if b, err := os.ReadFile(gd); err == nil {
		if s, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: "); ok {
			if !filepath.IsAbs(s) {
				s = filepath.Join(repo, s)
			}
			gd = s
		}
	}
	b, err := os.ReadFile(filepath.Join(gd, "HEAD"))
	if err != nil {
		return ""
	}
	h := strings.TrimSpace(string(b))
	if s, ok := strings.CutPrefix(h, "ref: refs/heads/"); ok {
		return s
	}
	if len(h) >= 8 {
		return h[:8]
	}
	return h
}

var (
	slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,59}$`)
	refRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
)

// ValidSlug / ValidRef check worktree names and base branches before they reach git.
func ValidSlug(s string) bool { return slugRe.MatchString(s) && !strings.Contains(s, "..") }
func ValidRef(s string) bool {
	return refRe.MatchString(s) && !strings.Contains(s, "..") && !strings.HasSuffix(s, ".lock") && !strings.HasSuffix(s, "/")
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// MakeWorktree makes <repo>-wt-<slug> next to the repo (the main checkout, even when dir is itself a
// worktree) on a new branch <slug> from base, and returns its path.
func MakeWorktree(ctx context.Context, r Roots, dir, base, slug string) (string, error) {
	if !ValidSlug(slug) {
		return "", fmt.Errorf("bad worktree name %q (lowercase letters, digits, . _ -)", slug)
	}
	if !ValidRef(base) {
		return "", fmt.Errorf("bad base branch %q", base)
	}
	common, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	main := filepath.Dir(common)
	if filepath.Base(common) != ".git" { // a bare repo: worktrees go next to it
		main = common
	}
	dest := filepath.Join(filepath.Dir(main), strings.TrimSuffix(filepath.Base(main), ".git")+"-wt-"+slug)
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("%s already exists", dest)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(dest))
	if err != nil || !r.inside(filepath.Join(parent, "x")) {
		return "", ErrOutside
	}
	if strings.HasPrefix(base, "origin/") { // best effort: start from what the remote has now
		_, _ = git(ctx, main, "fetch", "--quiet", "origin", strings.TrimPrefix(base, "origin/"))
	}
	if _, err := git(ctx, main, "worktree", "add", "-b", slug, dest, base); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(dest)
	if err != nil {
		return "", err
	}
	if !r.inside(real) {
		return "", ErrOutside
	}
	return real, nil
}

// DefaultBase is the branch a review compares against: origin's default branch, else main or master.
func DefaultBase(ctx context.Context, dir string) string {
	if s, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && ValidRef(s) {
		return s
	}
	for _, b := range []string{"origin/main", "origin/master", "main", "master"} {
		if _, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", b+"^{commit}"); err == nil {
			return b
		}
	}
	return "HEAD"
}

// ChangedFiles: files changed since base plus uncommitted and untracked ones.
func ChangedFiles(ctx context.Context, dir, base string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" && !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	if s, err := git(ctx, dir, "diff", "--name-only", base+"...HEAD"); err == nil {
		add(s)
	}
	if s, err := git(ctx, dir, "diff", "--name-only", "HEAD"); err == nil {
		add(s)
	}
	if s, err := git(ctx, dir, "ls-files", "--others", "--exclude-standard"); err == nil {
		add(s)
	}
	return out
}
