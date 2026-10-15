package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFSSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "proj")
	_ = os.MkdirAll(filepath.Join(root, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package a\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1"), 0o600)
	_ = os.WriteFile(filepath.Join(home, "outside.txt"), []byte("no"), 0o644)
	real, _ := filepath.EvalSymlinks(root)
	f := &fsSession{root: real, watched: map[string]string{}}
	str := func(s string) *string { return &s }

	if r := f.handle(fsReq{Op: "list"}); r.Error != "" || len(r.Entries) != 2 || !r.Entries[0].Dir {
		t.Fatalf("list: %+v", r)
	}
	rd := f.handle(fsReq{Op: "read", Path: "src/a.go"})
	if rd.Error != "" || *rd.Content != "package a\n" {
		t.Fatalf("read: %+v", rd)
	}
	if w := f.handle(fsReq{Op: "write", Path: "src/a.go", Content: str("package a // edited\n"), Base: rd.SHA}); w.Error != "" || w.Conflict {
		t.Fatalf("write: %+v", w)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "src", "a.go")); string(b) != "package a // edited\n" {
		t.Fatalf("on disk: %q", b)
	}
	// Someone else changed it: a write against the old sha is a conflict and changes nothing.
	if w := f.handle(fsReq{Op: "write", Path: "src/a.go", Content: str("stale\n"), Base: rd.SHA}); !w.Conflict {
		t.Fatalf("no conflict: %+v", w)
	}
	if w := f.handle(fsReq{Op: "write", Path: "src/new.go", Content: str("package a\n")}); w.Error != "" {
		t.Fatalf("new file: %+v", w)
	}
	for _, bad := range []string{"../outside.txt", "/etc/passwd", ".env", "src/../../outside.txt"} {
		r := f.handle(fsReq{Op: "read", Path: bad})
		if r.Error == "" && r.Content != nil && (*r.Content == "no" || *r.Content == "SECRET=1") {
			t.Fatalf("read %s: %+v", bad, r)
		}
	}
	f.handle(fsReq{Op: "write", Path: "../escape.txt", Content: str("x")}) // ".." is cut at the root
	if _, err := os.Stat(filepath.Join(home, "escape.txt")); err == nil {
		t.Fatal("wrote outside the root")
	}
	// A credential name or a key folder is refused even for a new file, and a deleted file is a conflict.
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	ks := &fsSession{root: home, watched: map[string]string{}}
	if w := ks.handle(fsReq{Op: "write", Path: ".ssh/authorized_keys", Content: str("x")}); w.Error == "" {
		t.Fatal("wrote into ~/.ssh")
	}
	_ = os.Symlink(filepath.Join(root, "src", "a.go"), filepath.Join(root, ".env.local"))
	if r := f.handle(fsReq{Op: "read", Path: ".env.local"}); r.Error == "" {
		t.Fatal("read through a credential-named link")
	}
	gone := f.handle(fsReq{Op: "read", Path: "src/new.go"})
	_ = os.Remove(filepath.Join(root, "src", "new.go"))
	if w := f.handle(fsReq{Op: "write", Path: "src/new.go", Content: str("x"), Base: gone.SHA}); !w.Conflict {
		t.Fatalf("deleted file recreated silently: %+v", w)
	}
	_ = os.WriteFile(filepath.Join(root, "latin1.txt"), []byte{'c', 'a', 'f', 0xe9}, 0o644)
	if r := f.handle(fsReq{Op: "read", Path: "latin1.txt"}); !r.Binary {
		t.Fatal("non-UTF-8 file offered for editing")
	}
	_ = os.Remove(filepath.Join(root, "latin1.txt"))
	if r := f.handle(fsReq{Op: "find"}); len(r.Files) != 2 { // escape.txt, src/a.go, src/new.go (.env hidden)
		t.Fatalf("find: %+v", r.Files)
	}
	// Watch: a change on disk is reported once.
	f.handle(fsReq{Op: "watch", Paths: []string{"src/a.go"}})
	f.handle(fsReq{Op: "read", Path: "src/a.go"})
	_ = os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("changed by claude\n"), 0o644)
	if ch := f.poll(); len(ch) != 1 || ch[0].Path != "src/a.go" {
		t.Fatalf("poll: %+v", ch)
	}
	if ch := f.poll(); len(ch) != 0 {
		t.Fatalf("reported twice: %+v", ch)
	}
}

func TestFSSessionManage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "proj")
	_ = os.MkdirAll(root, 0o755)
	real, _ := filepath.EvalSymlinks(root)
	f := &fsSession{root: real, watched: map[string]string{}}
	str := func(s string) *string { return &s }
	if r := f.handle(fsReq{Op: "create", Path: "docs/plan.md"}); r.Error != "" {
		t.Fatalf("create: %+v", r)
	}
	f.handle(fsReq{Op: "write", Path: "docs/plan.md", Content: str("alpha\nBeta gamma\nbeta\n")})
	if r := f.handle(fsReq{Op: "grep", Query: "beta"}); len(r.Hits) != 2 || r.Hits[0].Line != 2 || r.Hits[0].Col != 1 {
		t.Fatalf("grep: %+v", r.Hits)
	}
	if r := f.handle(fsReq{Op: "grep", Query: "beta", Case: true}); len(r.Hits) != 1 {
		t.Fatalf("grep case: %+v", r.Hits)
	}
	if r := f.handle(fsReq{Op: "grep", Query: `^\w+$`, Regex: true}); len(r.Hits) != 2 {
		t.Fatalf("grep regex: %+v", r.Hits)
	}
	if r := f.handle(fsReq{Op: "rename", Path: "docs/plan.md", To: "docs/roadmap.md"}); r.Error != "" {
		t.Fatalf("rename: %+v", r)
	}
	if r := f.handle(fsReq{Op: "delete", Path: "docs/roadmap.md"}); r.Error != "" {
		t.Fatalf("delete: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(home, ".Trash", "roadmap.md")); err != nil {
		t.Fatal("deleted file isn't in the Trash")
	}
	if r := f.handle(fsReq{Op: "delete", Path: ""}); r.Error == "" {
		t.Fatal("deleted the root")
	}
}
