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
	if r := f.handle(fsReq{Op: "find"}); len(r.Files) != 3 { // escape.txt, src/a.go, src/new.go (.env hidden)
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
