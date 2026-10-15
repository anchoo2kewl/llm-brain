package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// The editor's file channel: the hub queues fs_attach with a token, this agent dials the hub back (like a
// terminal attach) and then answers JSON requests over the websocket, fast enough to type against:
//
//	{"id":1,"op":"list","path":"src"}                 -> {"id":1,"entries":[{name,dir,size}]}
//	{"id":2,"op":"read","path":"src/a.go"}            -> {"id":2,"content":"…","sha":"…"} (or "binary":true)
//	{"id":3,"op":"write","path":"src/a.go","content":"…","base":"<sha>"} -> {"id":3,"sha":"…"} or {"conflict":true,…}
//	{"id":4,"op":"find"}                              -> {"id":4,"files":["src/a.go",…]} (for quick open)
//	{"id":5,"op":"watch","paths":["src/a.go"]}        -> {"id":5}; later {"op":"changed","path":…,"sha":…}
//
// Every path is relative to the editor's root folder and must stay inside it (and outside key folders).

const KindFSAttach = "fs_attach"

type FSAttach struct {
	Token string `json:"token"`
	Root  string `json:"root"`
}

type fsReq struct {
	ID      int64    `json:"id"`
	Op      string   `json:"op"`
	Path    string   `json:"path,omitempty"`
	To      string   `json:"to,omitempty"` // rename
	Dir     bool     `json:"dir,omitempty"` // create a folder
	Content *string  `json:"content,omitempty"`
	Base    string   `json:"base,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Query   string   `json:"query,omitempty"` // grep
	Regex   bool     `json:"regex,omitempty"`
	Case    bool     `json:"case,omitempty"`
}

// Hit is one find-in-files match.
type Hit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Text string `json:"text"`
}

type fsResp struct {
	ID       int64       `json:"id,omitempty"`
	Op       string      `json:"op,omitempty"`
	Error    string      `json:"error,omitempty"`
	Entries  []FileEntry `json:"entries,omitempty"`
	Content  *string     `json:"content,omitempty"`
	SHA      string      `json:"sha,omitempty"`
	Binary   bool        `json:"binary,omitempty"`
	Conflict bool        `json:"conflict,omitempty"`
	Deleted  bool        `json:"deleted,omitempty"` // a conflict because the file is gone (not just emptied)
	Files    []string    `json:"files,omitempty"`
	Path     string      `json:"path,omitempty"`
	Root     string      `json:"root,omitempty"`
	More     bool        `json:"more,omitempty"`
	Hits     []Hit       `json:"hits,omitempty"`
	Branch   string      `json:"branch,omitempty"`
	Size     int64       `json:"size,omitempty"`
	MTime    string      `json:"mtime,omitempty"`
	Mode     string      `json:"mode,omitempty"`
}

const (
	maxEditFile = 8 << 20
	maxFind     = 30000
)

// skipDirs aren't searched by quick open (still browsable in the tree).
var skipDirs = map[string]bool{".git": true, "node_modules": true, "target": true, "dist": true, "build": true, ".next": true,
	"vendor": true, ".venv": true, "__pycache__": true, ".cache": true, ".terraform": true}

type fsSession struct {
	root    string
	mu      sync.Mutex
	watched map[string]string // rel path -> last sha sent or read
}

var pathLocks sync.Map // real path -> *sync.Mutex

func lockPath(p string) func() {
	m, _ := pathLocks.LoadOrStore(p, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// resolve maps a path relative to the root to a real path inside it. A file that doesn't exist yet is
// resolved through its parent.
func (f *fsSession) resolve(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) {
		return "", ErrTraversal
	}
	p := filepath.Join(f.root, filepath.Clean("/"+rel))
	real, err := filepath.EvalSymlinks(p)
	if errors.Is(err, os.ErrNotExist) { // a new file (maybe in new folders): resolve the nearest existing parent
		dir, rest := filepath.Dir(p), filepath.Base(p)
		for {
			rd, err2 := filepath.EvalSymlinks(dir)
			if err2 == nil {
				real = filepath.Join(rd, rest)
				break
			}
			if !errors.Is(err2, os.ErrNotExist) || filepath.Dir(dir) == dir {
				return "", err
			}
			dir, rest = filepath.Dir(dir), filepath.Join(filepath.Base(dir), rest)
		}
	} else if err != nil {
		return "", err
	}
	if real != f.root && !strings.HasPrefix(real, f.root+string(filepath.Separator)) {
		return "", errors.New("outside the editor's folder")
	}
	// Key folders and credential names, on the path as asked and where it leads (also for a new file).
	if err := checkSecret(p); err != nil {
		return "", err
	}
	if err := checkSecret(real); err != nil {
		return "", err
	}
	return real, nil
}

// resolveEntry is resolve for operations on a directory entry itself (rename, delete): the parent is
// resolved, the last element is not, so a symlink is moved or trashed, never the file it points to.
func (f *fsSession) resolveEntry(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) {
		return "", ErrTraversal
	}
	clean := filepath.Clean("/" + rel)
	if clean == "/" {
		return f.root, nil
	}
	parent, err := f.resolve(filepath.Dir(clean)[1:])
	if err != nil {
		return "", err
	}
	p := filepath.Join(parent, filepath.Base(clean))
	if err := checkSecret(filepath.Join(f.root, clean)); err != nil {
		return "", err
	}
	if err := checkSecret(p); err != nil {
		return "", err
	}
	return p, nil
}

func (f *fsSession) handle(r fsReq) fsResp {
	out := fsResp{ID: r.ID}
	fail := func(err error) fsResp { out.Error = err.Error(); return out }
	switch r.Op {
	case "list":
		p, err := f.resolve(r.Path)
		if err != nil {
			return fail(err)
		}
		ents, err := os.ReadDir(p)
		if err != nil {
			return fail(err)
		}
		for _, d := range ents {
			fe := FileEntry{Name: d.Name()}
			if info, err := os.Stat(filepath.Join(p, d.Name())); err == nil {
				fe.Dir, fe.Size = info.IsDir(), info.Size()
			}
			out.Entries = append(out.Entries, fe)
		}
		sort.Slice(out.Entries, func(i, j int) bool {
			a, b := out.Entries[i], out.Entries[j]
			if a.Dir != b.Dir {
				return a.Dir
			}
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		})
	case "read":
		p, err := f.resolve(r.Path)
		if err != nil {
			return fail(err)
		}
		st, err := os.Stat(p)
		if err != nil {
			return fail(err)
		}
		if !st.Mode().IsRegular() {
			return fail(errors.New("not a regular file"))
		}
		if st.Size() > maxEditFile {
			return fail(fmt.Errorf("too big to edit here (%d MB)", st.Size()>>20))
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return fail(err)
		}
		out.SHA = sha(b)
		if strings.ContainsRune(string(b[:min(len(b), 8000)]), 0) || !utf8.Valid(b) { // shown read-only: a save would mangle it
			out.Binary = true
			return out
		}
		s := string(b)
		out.Content = &s
		f.mu.Lock()
		if _, ok := f.watched[r.Path]; ok {
			f.watched[r.Path] = out.SHA
		}
		f.mu.Unlock()
	case "write":
		if r.Content == nil {
			return fail(errors.New("no content"))
		}
		p, err := f.resolve(r.Path)
		if err != nil {
			return fail(err)
		}
		unlock := lockPath(p) // check and replace as one step, across every editor on this Mac
		defer unlock()
		cur, err := os.ReadFile(p)
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fail(err)
		}
		if !exists && r.Base != "" { // deleted on disk since the editor read it
			out.Conflict, out.Deleted = true, true
			return out
		}
		if exists && r.Base != "" && sha(cur) != r.Base { // changed on disk since the editor read it
			s := string(cur)
			out.Conflict, out.SHA, out.Content = true, sha(cur), &s
			return out
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(p); err == nil {
			mode = st.Mode().Perm()
		}
		tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".hub-*")
		if err != nil {
			return fail(err)
		}
		_, werr := tmp.WriteString(*r.Content)
		cerr := tmp.Close()
		if werr != nil || cerr != nil {
			os.Remove(tmp.Name())
			return fail(errors.Join(werr, cerr))
		}
		_ = os.Chmod(tmp.Name(), mode)
		if err := os.Rename(tmp.Name(), p); err != nil {
			os.Remove(tmp.Name())
			return fail(err)
		}
		out.SHA = sha([]byte(*r.Content))
		f.mu.Lock()
		if _, ok := f.watched[r.Path]; ok {
			f.watched[r.Path] = out.SHA
		}
		f.mu.Unlock()
	case "find":
		err := filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != f.root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || secretFile(d.Name()) {
				return nil
			}
			if len(out.Files) >= maxFind {
				out.More = true
				return filepath.SkipAll
			}
			rel, _ := filepath.Rel(f.root, p)
			out.Files = append(out.Files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return fail(err)
		}
	case "watch":
		f.mu.Lock()
		next := map[string]string{}
		for _, p := range r.Paths {
			if len(next) >= 200 {
				break
			}
			next[p] = f.watched[p]
		}
		f.watched = next
		f.mu.Unlock()
	case "hello":
		out.Root = f.root
		out.Branch = gitBranchUp(f.root)
	case "stat":
		p, err := f.resolve(r.Path)
		if err != nil {
			return fail(err)
		}
		st, err := os.Stat(p)
		if err != nil {
			return fail(err)
		}
		out.Size, out.MTime, out.Mode = st.Size(), st.ModTime().UTC().Format(time.RFC3339), st.Mode().String()
		out.Branch = gitBranchUp(filepath.Dir(p))
	case "create":
		p, err := f.resolve(r.Path)
		if err != nil {
			return fail(err)
		}
		if _, err := os.Lstat(p); err == nil {
			return fail(errors.New("already exists"))
		}
		if r.Dir {
			err = os.MkdirAll(p, 0o755)
		} else {
			if err = os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
				var fh *os.File
				if fh, err = os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644); err == nil {
					err = fh.Close()
				}
			}
		}
		if err != nil {
			return fail(err)
		}
	case "rename":
		from, err := f.resolveEntry(r.Path)
		if err != nil {
			return fail(err)
		}
		to, err := f.resolveEntry(r.To)
		if err != nil {
			return fail(err)
		}
		if from == f.root {
			return fail(errors.New("can't rename the folder itself"))
		}
		if _, err := os.Lstat(to); err == nil {
			return fail(errors.New(r.To + " already exists"))
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return fail(err)
		}
		if err := os.Rename(from, to); err != nil {
			return fail(err)
		}
	case "delete": // to the Trash, so a slip can be undone from Finder
		p, err := f.resolveEntry(r.Path)
		if err != nil {
			return fail(err)
		}
		if p == f.root {
			return fail(errors.New("can't delete the folder itself"))
		}
		home, _ := os.UserHomeDir()
		trash := filepath.Join(home, ".Trash")
		if err := os.MkdirAll(trash, 0o700); err != nil {
			return fail(err)
		}
		if _, err := os.Lstat(p); err != nil {
			return fail(err)
		}
		// A name nothing else in the Trash has: the name itself, else with a time and random suffix.
		dest := filepath.Join(trash, filepath.Base(p))
		for i := 0; ; i++ {
			if _, err := os.Lstat(dest); errors.Is(err, os.ErrNotExist) {
				break
			}
			var rb [4]byte
			_, _ = rand.Read(rb[:])
			dest = filepath.Join(trash, fmt.Sprintf("%s (hub %s %x)", filepath.Base(p), time.Now().Format("20060102-150405"), rb))
			if i > 5 {
				return fail(errors.New("couldn't find a free name in the Trash"))
			}
		}
		if err := os.Rename(p, dest); err != nil {
			return fail(err)
		}
	case "grep":
		return f.grep(r, out)
	default:
		return fail(fmt.Errorf("unknown op %q", r.Op))
	}
	return out
}

// poll checks the watched files and reports the ones whose content changed on disk.
func (f *fsSession) poll() []fsResp {
	f.mu.Lock()
	paths := map[string]string{}
	for p, s := range f.watched {
		paths[p] = s
	}
	f.mu.Unlock()
	var out []fsResp
	for rel, last := range paths {
		p, err := f.resolve(rel)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil || len(b) > maxEditFile {
			continue
		}
		s := sha(b)
		if s == last {
			continue
		}
		f.mu.Lock()
		if f.watched[rel] == last {
			f.watched[rel] = s
		}
		f.mu.Unlock()
		if last != "" {
			out = append(out, fsResp{Op: "changed", Path: rel, SHA: s})
		}
	}
	return out
}

func (e *Executor) fsAttach(ctx context.Context, c store.Command) {
	var a FSAttach
	if err := json.Unmarshal(c.Payload, &a); err != nil || a.Token == "" {
		e.report(ctx, c.ID, Report{Status: "failed", Error: "bad payload"})
		return
	}
	fail := func(err error) { e.report(ctx, c.ID, Report{Status: "failed", Error: err.Error()}) }
	root, err := HomePath(a.Root)
	if err != nil {
		fail(err)
		return
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		fail(errors.New("not a folder"))
		return
	}
	u, err := url.Parse(strings.TrimRight(e.HubURL, "/") + "/api/agent/term/" + url.PathEscape(a.Token))
	if err != nil {
		fail(err)
		return
	}
	u.Scheme = map[string]string{"https": "wss"}[u.Scheme]
	if u.Scheme == "" {
		u.Scheme = "ws"
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	ws, _, err := websocket.Dial(dctx, u.String(), nil)
	cancel()
	if err != nil {
		fail(fmt.Errorf("dial hub: %w", err))
		return
	}
	ws.SetReadLimit(maxEditFile * 2)
	e.report(ctx, c.ID, Report{Status: "done"})
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer ws.CloseNow()
		f := &fsSession{root: root, watched: map[string]string{}}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var wmu sync.Mutex
		send := func(r fsResp) error {
			b, _ := json.Marshal(r)
			wmu.Lock()
			defer wmu.Unlock()
			wctx, wc := context.WithTimeout(ctx, 30*time.Second)
			defer wc()
			return ws.Write(wctx, websocket.MessageText, b)
		}
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					for _, ch := range f.poll() {
						if send(ch) != nil {
							cancel()
							return
						}
					}
				}
			}
		}()
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var r fsReq
			if json.Unmarshal(b, &r) != nil {
				continue
			}
			if send(f.handle(r)) != nil {
				return
			}
		}
	}()
}

// gitBranchUp is the branch of the repo dir is in (walking up), or "".
func gitBranchUp(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return gitBranch(d)
		}
		if p := filepath.Dir(d); p == d {
			return ""
		}
	}
}

const (
	maxHits      = 2000
	maxGrepFile  = 2 << 20
	maxGrepFiles = 50000
)

// grep finds a string (or a regexp) in every text file under the root, Sublime's Find in Files.
func (f *fsSession) grep(r fsReq, out fsResp) fsResp {
	if strings.TrimSpace(r.Query) == "" {
		out.Error = "nothing to find"
		return out
	}
	pat := r.Query
	if !r.Regex {
		pat = regexp.QuoteMeta(pat)
	}
	if !r.Case {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		out.Error = "bad pattern: " + err.Error()
		return out
	}
	seen := 0
	start := time.Now()
	_ = filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != f.root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") || checkSecret(p) != nil) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || checkSecret(p) != nil {
			return nil
		}
		if seen++; seen > maxGrepFiles || len(out.Hits) >= maxHits || time.Since(start) > 20*time.Second {
			out.More = true
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxGrepFile {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil || strings.ContainsRune(string(b[:min(len(b), 8000)]), 0) || !utf8.Valid(b) {
			return nil
		}
		rel, _ := filepath.Rel(f.root, p)
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(string(b), "\n") {
			if loc := re.FindStringIndex(line); loc != nil {
				text := line
				if len(text) > 300 {
					from := max(0, loc[0]-100)
					text = text[from:min(len(text), from+300)]
				}
				out.Hits = append(out.Hits, Hit{Path: rel, Line: i + 1, Col: utf8.RuneCountInString(line[:loc[0]]) + 1, Text: text})
				if len(out.Hits) >= maxHits {
					out.More = true
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return out
}
