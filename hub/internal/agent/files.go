package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Files: list a folder, download a file, or receive an upload, on this host, for the hub's pages. Like
// terminals, the hub queues a command with a one-time token and this agent talks back over HTTP:
//
//	file_list  POST the listing (JSON) to /api/agent/file/{token}
//	file_get   POST the file's bytes to /api/agent/file/{token}
//	file_put   GET the upload from /api/agent/file/{token} and save it under <state>/uploads/<dir>
//
// Paths are limited to the home folder (a terminal is a full shell there already), minus the places
// that hold keys.

const (
	KindFileList = "file_list"
	KindFileGet  = "file_get"
	KindFilePut  = "file_put"
)

// FileReq is the payload of every file command.
type FileReq struct {
	Token string `json:"token"`
	Path  string `json:"path,omitempty"` // list/get: absolute or ~/...
	Into  string `json:"into,omitempty"` // put: a subfolder of <state>/uploads, e.g. "t4"
	Name  string `json:"name,omitempty"` // put: the file's name
}

// FileEntry is one row of a listing.
type FileEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir,omitempty"`
	Size  int64  `json:"size,omitempty"`
	MTime string `json:"mtime,omitempty"`
}

// Listing is what file_list sends.
type Listing struct {
	Path    string      `json:"path"` // the real path
	Home    string      `json:"home"`
	Entries []FileEntry `json:"entries"`
	More    bool        `json:"more,omitempty"`
}

// secretDirs are never listed or served, even under home, whether asked for directly or through a link.
var secretDirs = []string{".ssh", ".aws", ".gnupg", ".kube", ".docker", ".azure", ".config/llm-brain", ".config/gh", ".config/gcloud",
	".config/op", ".config/elastio", ".codex", ".claude", ".password-store", "Library/Keychains", "Library/Application Support/1Password"}

// secretFile: names that hold credentials wherever they are.
func secretFile(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case ".env", ".netrc", ".npmrc", ".pypirc", ".git-credentials", ".claude.json", "credentials", "token.txt", "auth.json":
		return true
	}
	for _, p := range []string{".env.", "id_rsa", "id_ed25519", "id_ecdsa"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, x := range []string{".env", ".pem", ".key", ".p12", ".pfx", ".keychain", ".keychain-db", ".kdbx"} {
		if strings.HasSuffix(n, x) {
			return true
		}
	}
	return false
}

func underSecret(rel string) (string, bool) {
	for _, d := range secretDirs {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return d, true
		}
	}
	return "", false
}

// checkSecret refuses key folders and credential files for a path, lexically (as asked) and through links:
// a key folder that is itself a link counts by its target too.
func checkSecret(p string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	realHome, _ := filepath.EvalSymlinks(home)
	for _, h := range []string{home, realHome} {
		if r, err := filepath.Rel(h, p); err == nil && !strings.HasPrefix(r, "..") {
			if d, bad := underSecret(r); bad {
				return fmt.Errorf("~/%s holds keys; the hub won't serve it", d)
			}
		}
	}
	for _, d := range secretDirs {
		if t, err := filepath.EvalSymlinks(filepath.Join(home, d)); err == nil && (p == t || strings.HasPrefix(p, t+string(filepath.Separator))) {
			return fmt.Errorf("~/%s holds keys; the hub won't serve it", d)
		}
	}
	if secretFile(filepath.Base(p)) {
		return fmt.Errorf("%s looks like a credential; the hub won't serve it", filepath.Base(p))
	}
	return nil
}

// HomePath resolves p (absolute or ~/...) to a real path in the home folder, refusing the key folders and
// credential files, checked on both the path asked for and where it really leads.
func HomePath(p string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	if p == "" {
		p = "~"
	}
	p = filepath.Clean(expandHome(p))
	if !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
		return "", ErrTraversal
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, os.ErrNotExist)
	}
	if real != realHome && !strings.HasPrefix(real, realHome+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is not in your home folder", p)
	}
	if err := checkSecret(p); err != nil {
		return "", err
	}
	if err := checkSecret(real); err != nil {
		return "", err
	}
	return real, nil
}

func (e *Executor) fileURL(tok string) string {
	return strings.TrimRight(e.HubURL, "/") + "/api/agent/file/" + url.PathEscape(tok)
}

var fileClient = &http.Client{Timeout: 30 * time.Minute}

func (e *Executor) fileCmd(ctx context.Context, c store.Command) {
	var r FileReq
	if err := json.Unmarshal(c.Payload, &r); err != nil || r.Token == "" {
		e.report(ctx, c.ID, Report{Status: "failed", Error: "bad payload"})
		return
	}
	var res string
	var err error
	switch c.Kind {
	case KindFileList:
		err = e.fileList(ctx, r)
	case KindFileGet:
		err = e.fileGet(ctx, r)
	case KindFilePut:
		res, err = e.filePut(ctx, r)
	}
	if err != nil {
		e.logf("agent: %s %q: %v", c.Kind, r.Path+r.Name, err)
		e.report(ctx, c.ID, Report{Status: "failed", Error: err.Error()})
		return
	}
	e.report(ctx, c.ID, Report{Status: "done", Result: res})
}

func (e *Executor) post(ctx context.Context, tok, ctype string, body io.Reader, size int64, hdr map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.fileURL(tok), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)
	if size >= 0 {
		req.ContentLength = size
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := fileClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("hub: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (e *Executor) fileList(ctx context.Context, r FileReq) error {
	real, err := HomePath(r.Path)
	if err != nil {
		return err
	}
	ents, err := os.ReadDir(real)
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	home, _ = filepath.EvalSymlinks(home)
	l := Listing{Path: real, Home: home}
	for _, d := range ents {
		if len(l.Entries) >= 2000 {
			l.More = true
			break
		}
		fe := FileEntry{Name: d.Name()}
		if info, err := os.Stat(filepath.Join(real, d.Name())); err == nil { // follow links
			fe.Dir, fe.Size, fe.MTime = info.IsDir(), info.Size(), info.ModTime().UTC().Format(time.RFC3339)
		}
		l.Entries = append(l.Entries, fe)
	}
	sort.Slice(l.Entries, func(i, j int) bool {
		a, b := l.Entries[i], l.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	b, _ := json.Marshal(l)
	return e.post(ctx, r.Token, "application/json", strings.NewReader(string(b)), int64(len(b)), nil)
}

func (e *Executor) fileGet(ctx context.Context, r FileReq) error {
	real, err := HomePath(r.Path)
	if err != nil {
		return err
	}
	// O_NONBLOCK: opening a FIFO must not hang; anything but a regular file is refused once open.
	f, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return errors.New("that's a folder")
	}
	if !st.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if err := syscall.SetNonblock(int(f.Fd()), false); err != nil {
		return err
	}
	return e.post(ctx, r.Token, "application/octet-stream", f, st.Size(), map[string]string{"X-File-Name": filepath.Base(real), "X-File-Path": real})
}

// uploadsDir is <state>/uploads: outside every repo, so a dropped file never ends up in a commit.
func (e *Executor) uploadsDir() string { return filepath.Join(e.StateDir, "uploads") }

// SafeName keeps a dropped file's name to plain characters.
func SafeName(n string) string {
	n = filepath.Base(strings.ReplaceAll(n, "\\", "/"))
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	s := strings.TrimLeft(b.String(), ".")
	if len(s) > 100 {
		s = s[len(s)-100:]
	}
	if s == "" {
		s = "file"
	}
	return s
}

func (e *Executor) filePut(ctx context.Context, r FileReq) (string, error) {
	if !ValidSlug(r.Into) {
		return "", fmt.Errorf("bad upload folder %q", r.Into)
	}
	base := filepath.Join(e.uploadsDir(), r.Into)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(base, time.Now().Format("20060102-150405")+"-") // one per upload: pasted image.png twice is fine
	if err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0o755)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.fileURL(r.Token), nil)
	if err != nil {
		return "", err
	}
	resp, err := fileClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("hub: %s", resp.Status)
	}
	dest := filepath.Join(dir, SafeName(r.Name))
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(dest)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return dest, nil
}
