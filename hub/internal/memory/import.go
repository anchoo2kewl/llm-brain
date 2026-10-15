package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Import: Claude Code's own memory files (~/.claude/projects/<folder>/memory/*.md, one fact per file with
// name/description/type frontmatter) become shared memories. It runs on the Mac that has the files and posts
// each one to the hub, which makes it idempotent: the same file unchanged is a duplicate (no-op), the same
// file edited supersedes its earlier import. Run it again any time to keep them in step.

// ClaudeFile is one parsed memory file.
type ClaudeFile struct {
	Path        string
	Project     string // the folder under ~/.claude/projects (the encoded working directory)
	Name        string
	Description string
	Type        string // user | feedback | project | reference
	Body        string
}

var kindOfType = map[string]string{"user": KindFact, "feedback": KindDecision, "project": KindFact, "reference": KindRunbook}

// ParseClaudeFile reads frontmatter (top-level type, or metadata.type) and the body.
func ParseClaudeFile(path string, data []byte) (ClaudeFile, error) {
	f := ClaudeFile{Path: path}
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return f, errors.New("no frontmatter")
	}
	fm, body, ok := strings.Cut(s[4:], "\n---")
	if !ok {
		return f, errors.New("frontmatter not closed")
	}
	f.Body = strings.TrimSpace(strings.TrimPrefix(body, "\n"))
	for _, line := range strings.Split(fm, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			f.Name = v
		case "description":
			f.Description = v
		case "type":
			f.Type = strings.ToLower(v)
		}
	}
	if f.Body == "" && f.Description == "" {
		return f, errors.New("empty")
	}
	return f, nil
}

// ToWrite turns a file into a memory write for this host.
func (f ClaudeFile) ToWrite(host, folder, repo string) Write {
	title := f.Description
	if title == "" {
		title = f.Name
	}
	title = strings.TrimSpace(strings.Split(title, "\n")[0])
	title = clip(title, MaxTitle)
	body := f.Body
	if body == "" {
		body = f.Description
	}
	if len(body) > MaxBody {
		note := "\n\n(shortened; the full text is " + f.Path + " on " + host + ")"
		body = strings.TrimSpace(clip(body, MaxBody-len(note))) + note
	}
	kind := kindOfType[f.Type]
	if kind == "" {
		kind = KindFact
	}
	return Write{Kind: kind, Title: title, Body: body, Scope: Scope{Repo: repo, Folder: folder},
		Author: Author{Agent: "claude-code memory", Provider: "claude", Host: host}, Confidence: "high",
		ExpiresDays: -1, Source: "import", SourcePath: f.Path}
}

// clip cuts s to at most n bytes on a rune boundary, ending in … when it cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

// DecodeProjectDir turns Claude's encoded folder name (/ and . become -) back into a path that exists, trying
// each dash as a separator or a literal dash/dot. It returns the naive decoding when nothing on disk matches.
func DecodeProjectDir(enc string) string {
	parts := strings.Split(strings.TrimPrefix(enc, "-"), "-")
	if p := resolve("/", parts); p != "" {
		return p
	}
	return "/" + strings.Join(parts, "/")
}

func resolve(dir string, parts []string) string {
	if len(parts) == 0 {
		return filepath.Clean(dir)
	}
	// Longest name first: "llm-brain" before "llm". An empty part is a dot: /.claude is encoded --claude.
	for n := len(parts); n >= 1; n-- {
		seg, lead := parts[:n], ""
		if seg[0] == "" {
			if n < 2 {
				continue
			}
			seg, lead = seg[1:], "."
		}
		seps := []string{"-"}
		if len(seg) > 1 {
			seps = append(seps, ".")
		}
		for _, sep := range seps {
			p := filepath.Join(dir, lead+strings.Join(seg, sep))
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				if r := resolve(p, parts[n:]); r != "" {
					return r
				}
			}
		}
	}
	return ""
}

var remoteRe = regexp.MustCompile(`github\.com[:/]([^/\s]+/[^/\s]+?)(\.git)?$`)

// RepoOf is the GitHub owner/name of a folder's origin remote, or "".
func RepoOf(ctx context.Context, folder string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", folder, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	if m := remoteRe.FindStringSubmatch(strings.TrimSpace(string(out))); m != nil {
		return m[1]
	}
	return ""
}

// ImportStats is what an import did.
type ImportStats struct {
	Files, Created, Superseded, Duplicate, Refused, Failed int
	Errors                                                 []string
}

// Poster sends one write to the hub.
type Poster func(ctx context.Context, w Write) (Result, error)

// ImportClaude imports every memory file under dir (~/.claude/projects).
func ImportClaude(ctx context.Context, dir, host string, post Poster) (ImportStats, error) {
	var st ImportStats
	files, err := filepath.Glob(filepath.Join(dir, "*", "memory", "*.md"))
	if err != nil {
		return st, err
	}
	sort.Strings(files)
	folders := map[string][2]string{}
	for _, p := range files {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		if strings.EqualFold(filepath.Base(p), "MEMORY.md") {
			continue // the index, not a memory
		}
		st.Files++
		data, err := os.ReadFile(p)
		if err != nil {
			st.Failed++
			st.Errors = append(st.Errors, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		f, err := ParseClaudeFile(p, data)
		if err != nil {
			st.Refused++
			st.Errors = append(st.Errors, fmt.Sprintf("%s: %v", p, err))
			continue
		}
		f.Project = filepath.Base(filepath.Dir(filepath.Dir(p)))
		fr, ok := folders[f.Project]
		if !ok {
			folder := DecodeProjectDir(f.Project)
			fr = [2]string{folder, RepoOf(ctx, folder)}
			folders[f.Project] = fr
		}
		res, err := post(ctx, f.ToWrite(host, fr[0], fr[1]))
		var ie InputError
		switch {
		case errors.As(err, &ie):
			st.Refused++
			st.Errors = append(st.Errors, fmt.Sprintf("%s: %v", p, err))
		case err != nil:
			st.Failed++
			st.Errors = append(st.Errors, fmt.Sprintf("%s: %v", p, err))
		case res.Outcome == "duplicate":
			st.Duplicate++
		case res.Outcome == "superseded":
			st.Superseded++
		default:
			st.Created++
		}
	}
	return st, nil
}

// HTTPPoster posts writes to a hub's /api/memory.
func HTTPPoster(hub string) Poster {
	hc := &http.Client{Timeout: 30 * time.Second}
	return func(ctx context.Context, w Write) (Result, error) {
		b, _ := json.Marshal(w)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(hub, "/")+"/api/memory", bytes.NewReader(b))
		if err != nil {
			return Result{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			return Result{}, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		switch {
		case resp.StatusCode == http.StatusUnprocessableEntity:
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(body, &e)
			return Result{}, InputError(e.Error)
		case resp.StatusCode/100 != 2:
			return Result{}, fmt.Errorf("hub: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var r Result
		return r, json.Unmarshal(body, &r)
	}
}
