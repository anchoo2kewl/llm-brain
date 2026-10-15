package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
	Content *string  `json:"content,omitempty"`
	Base    string   `json:"base,omitempty"`
	Paths   []string `json:"paths,omitempty"`
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
	Files    []string    `json:"files,omitempty"`
	Path     string      `json:"path,omitempty"`
	Root     string      `json:"root,omitempty"`
	More     bool        `json:"more,omitempty"`
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

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// resolve maps a path relative to the root to a real path inside it. A file that doesn't exist yet is
// resolved through its parent.
func (f *fsSession) resolve(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) {
		return "", ErrTraversal
	}
	p := filepath.Join(f.root, filepath.Clean("/"+rel))
	real, err := filepath.EvalSymlinks(p)
	if errors.Is(err, os.ErrNotExist) {
		dir, err2 := filepath.EvalSymlinks(filepath.Dir(p))
		if err2 != nil {
			return "", err
		}
		real = filepath.Join(dir, filepath.Base(p))
	} else if err != nil {
		return "", err
	}
	if real != f.root && !strings.HasPrefix(real, f.root+string(filepath.Separator)) {
		return "", errors.New("outside the editor's folder")
	}
	if _, err := os.Stat(real); err == nil {
		if _, err := HomePath(real); err != nil { // key folders and credential files
			return "", err
		}
	} else if secretFile(filepath.Base(real)) {
		return "", fmt.Errorf("%s looks like a credential", filepath.Base(real))
	}
	return real, nil
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
		if strings.ContainsRune(string(b[:min(len(b), 8000)]), 0) {
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
		cur, err := os.ReadFile(p)
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fail(err)
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
