package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTerminalLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	open := func() (int64, int64) {
		id, err := s.CreateTerminal(ctx, Terminal{Host: "studio", Provider: "claude", Cwd: "~/app"}, Command{Host: "studio", Kind: "term_open"})
		if err != nil {
			t.Fatal(err)
		}
		cs, _ := s.Commands(ctx, id, 5)
		return id, cs[0].ID
	}
	status := func(id int64) Terminal {
		tm, err := s.Terminal(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}

	// The report finishes the command and moves the terminal together; a retry changes nothing.
	a, ca := open()
	if err := s.ApplyTermReport(ctx, ca, "done", "{}", a, "live", "/Users/me/app", ""); err != nil {
		t.Fatal(err)
	}
	if tm := status(a); tm.Status != "live" || tm.Cwd != "/Users/me/app" {
		t.Fatalf("after open: %+v", tm)
	}
	if err := s.ApplyTermReport(ctx, ca, "failed", "{}", a, "failed", "", "late"); err != nil {
		t.Fatal(err)
	}
	if status(a).Status != "live" {
		t.Fatal("a late report moved a finished command's terminal")
	}

	// A listing taken before the terminal went live doesn't end it; a later one does.
	before := T(time.Now().Add(-time.Minute))
	if _, _, err := s.SyncTerminals(ctx, "studio", nil, before); err != nil {
		t.Fatal(err)
	}
	if status(a).Status != "live" {
		t.Fatal("an old listing ended a terminal that went live after it")
	}
	time.Sleep(1100 * time.Millisecond)
	if n, _, _ := s.SyncTerminals(ctx, "studio", nil, Now()); n != 1 || status(a).Status != "ended" {
		t.Fatalf("missing terminal not ended: %d %s", n, status(a).Status)
	}

	// An open failed by the lost-command check comes back when tmux still has it.
	b, cb := open()
	if err := s.ApplyTermReport(ctx, cb, "failed", "{}", b, "failed", "", "lost"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, r, _ := s.SyncTerminals(ctx, "studio", []TermSeen{{ID: b, Cwd: "/Users/me/app"}}, Now()); r != 1 {
		t.Fatal("surviving tmux session not revived")
	}
	if tm := status(b); tm.Status != "live" || tm.Cwd != "/Users/me/app" || tm.Error != "" {
		t.Fatalf("revived: %+v", tm)
	}
	// ...but a listing older than an End doesn't bring it back.
	if err := s.SetTerminal(ctx, b, "ended", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, r, _ := s.SyncTerminals(ctx, "studio", []TermSeen{{ID: b}}, before); r != 0 || status(b).Status != "ended" {
		t.Fatal("an old listing revived a terminal ended after it")
	}

	// End before any host claimed the open: the open is cancelled.
	c, cc := open()
	if opened, err := s.CancelQueuedTerm(ctx, c); err != nil || !opened {
		t.Fatalf("cancel queued open: %v %v", opened, err)
	}
	if cmd, _ := s.Command(ctx, cc); cmd.Status != "cancelled" {
		t.Fatalf("open command %s", cmd.Status)
	}
	if cs, _ := s.ClaimCommands(ctx, "studio"); len(cs) != 0 {
		t.Fatalf("a cancelled open was claimed: %+v", cs)
	}
}

func TestRecentFoldersLiteralPrefix(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, cwd := range []string{"/Users/a_b/app", "/Users/axb/other", "/Users/a_b", "/Users/rené/x"} {
		if _, err := s.CreateTerminal(ctx, Terminal{Host: "h", Provider: "shell", Cwd: cwd}, Command{Host: "h", Kind: "term_open"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RecentFolders(ctx, "h", "/Users/a_b", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "/Users/a_b/app" {
		t.Fatalf("recent: %v", got)
	}
	if got, _ := s.RecentFolders(ctx, "h", "/Users/rené", 10); len(got) != 1 || got[0] != "/Users/rené/x" {
		t.Fatalf("unicode home: %v", got)
	}
}

func TestSessionSearchFindsHubName(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`INSERT INTO sessions (id, host, path, provider, title, message_count) VALUES (5, 'h', '/a.jsonl', 'claude', 'first prompt', 2)`); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameSession(ctx, 5, "Meircom ransomware"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Sessions(ctx, SessionFilter{Q: "meircom"})
	if err != nil || len(got) != 1 || got[0].DisplayTitle() != "Meircom ransomware" {
		t.Fatalf("search: %+v %v", got, err)
	}
}
