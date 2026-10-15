package web

import (
	"context"
	"net/url"
	"testing"
)

func TestCleanNameAndClaudeIdle(t *testing.T) {
	if n, err := cleanName("  fix\tthe   pager\n"); err != nil || n != "fix the pager" {
		t.Fatalf("cleanName: %q %v", n, err)
	}
	if _, err := cleanName("   "); err == nil {
		t.Fatal("empty name accepted")
	}
	idle := "╭────╮\n❯ Try \"how do I log an error?\"\n────\n  ⏵⏵ bypass permissions on"
	busy := "✶ Crunching… (7s · esc to interrupt)\n❯ \n"
	typing := "────\n❯ half a sentence\n────"
	empty := "────\n❯ \n────"
	for s, want := range map[string]bool{idle: true, busy: false, typing: false, empty: true, "no prompt here": false} {
		if got := claudeIdle(s); got != want {
			t.Errorf("claudeIdle(%q) = %v", s, got)
		}
	}
}

func TestRenameSessionAndChat(t *testing.T) {
	rig := newSessRig(t)
	ctx := context.Background()
	if _, err := rig.st.DB.Exec(`INSERT INTO sessions (id, host, path, provider, session_uuid, cwd, title, message_count) VALUES (40, 'studio', '/a.jsonl', 'claude', 'u40', '/x', 'first prompt', 3)`); err != nil {
		t.Fatal(err)
	}
	resp, err := rig.cl.PostForm(rig.ts.URL+"/sessions/40/rename", url.Values{"title": {"Meircom ransomware check"}})
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("rename: %v %v", err, resp)
	}
	x, _ := rig.st.Session(ctx, 40)
	if x.DisplayTitle() != "Meircom ransomware check" {
		t.Fatalf("title %q", x.DisplayTitle())
	}
	// Ingest rewrites title/auto_title; the hub's name still wins.
	_, _ = rig.st.DB.Exec(`UPDATE sessions SET title = 'other', auto_title = 'ai title' WHERE id = 40`)
	if x, _ := rig.st.Session(ctx, 40); x.DisplayTitle() != "Meircom ransomware check" {
		t.Fatalf("after ingest %q", x.DisplayTitle())
	}
	if resp, _ := rig.cl.PostForm(rig.ts.URL+"/sessions/40/rename", url.Values{"title": {""}}); resp.StatusCode != 400 {
		t.Fatalf("empty rename: %d", resp.StatusCode)
	}
}
