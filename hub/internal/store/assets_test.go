package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSessionFilesAndWrittenDocs(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`INSERT INTO sessions (id, host, path, provider, session_uuid, cwd) VALUES (7, 'studio', '/x.jsonl', 'claude', 'u7', '/Users/me/elastio'),
		(8, 'studio', '/y.jsonl', 'claude', 'u8', '/Users/me/elastio')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE sessions SET parent_uuid = 'u7' WHERE id = 8`); err != nil {
		t.Fatal(err)
	}
	add := func(idx int, tool, input string) { addIn(t, s, 7, idx, tool, input) }
	add(1, "Write", `{"file_path":"/Users/me/elastio/report.md","content":"x"}`)
	add(2, "Edit", `{"file_path":"/Users/me/elastio/repo/main.go","old_string":"a","new_string":"b"}`)
	add(3, "Read", `{"file_path":"/Users/me/elastio/notes.md"}`)
	add(4, "apply_patch", `{"input":"*** Begin Patch\n*** Add File: /Users/me/out.csv\n+a\n*** Update File: /Users/me/elastio/repo/x.go\n*** End Patch"}`)
	add(5, "Edit", `{"file_path":"/Users/me/elastio/report.md","old_string":"x","new_string":"y"}`)
	add(6, "Write", `{"file_path":"/Users/me/big.pdf","content":"cut short by ingestion…`) // truncated JSON
	add(7, "apply_patch", `*** Begin Patch\n*** Add File: docs/plan.md\n+x\n*** End Patch`) // relative to cwd
	addIn(t, s, 8, 1, "Write", `{"file_path":"/Users/me/sub.md","content":"x"}`)            // a subagent's file
	fs, err := s.SessionFiles(ctx, Session{ID: 7, Host: "studio", UUID: "u7"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range fs {
		got[f.Path] = f.Created
	}
	want := map[string]bool{"/Users/me/elastio/report.md": true, "/Users/me/elastio/repo/main.go": false, "/Users/me/out.csv": true,
		"/Users/me/elastio/repo/x.go": false, "/Users/me/big.pdf": true, "/Users/me/elastio/docs/plan.md": true, "/Users/me/sub.md": true}
	if len(got) != len(want) {
		t.Fatalf("files %v", got)
	}
	for p, c := range want {
		if v, ok := got[p]; !ok || v != c {
			t.Fatalf("%s: %v %v (all %v)", p, v, ok, got)
		}
	}
	if fs[0].Path != "/Users/me/sub.md" {
		t.Fatalf("most recent first: %+v", fs)
	}
	docs, last, err := s.WrittenDocs(ctx, 0, 100)
	if err != nil || len(docs) != 5 || docs[0] != [2]string{"studio", "/Users/me/elastio/report.md"} || last == 0 {
		t.Fatalf("docs %v %d %v", docs, last, err)
	}
	if docs, _, _ := s.WrittenDocs(ctx, last, 100); len(docs) != 0 {
		t.Fatalf("cursor: %v", docs)
	}
}

func addIn(t *testing.T, s *Store, sess int64, idx int, tool, input string) {
	t.Helper()
	if _, err := s.DB.Exec(`INSERT INTO messages (session_id, line_off, idx, role, kind, tool_name, tool_input) VALUES (?, ?, 0, 'assistant', 'tool_use', ?, ?)`,
		sess, idx, tool, input); err != nil {
		t.Fatal(err)
	}
}
