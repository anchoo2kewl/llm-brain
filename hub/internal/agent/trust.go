package agent

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// A CLI opened in a folder it hasn't seen asks whether to trust it (Claude Code: "Yes, I trust this folder";
// Codex: "Do you trust the contents of this directory?"; Gemini: "Do you trust this folder?"). Nobody is at
// the Mac's screen, so a hub terminal would sit there until someone answered from the browser. The person
// picked the folder when they opened the terminal, so the hub answers yes for them.

var (
	trustAsk = regexp.MustCompile(`(?i)(trust this folder|trust the contents of this directory|trust the files in this folder|do you trust)`)
	// The option that trusts this folder (not its parent, and not "don't trust").
	trustYes = regexp.MustCompile(`(?i)^\W*(\d+\.\s*)?(yes\b.*(trust|continue|proceed)|trust (this )?folder\b|trust and continue)`)
	trustNo  = regexp.MustCompile(`(?i)(\bno\b|don'?t trust|do not trust|exit|quit|parent folder)`)
	// The cursor at the start of an option line.
	trustCursor = regexp.MustCompile(`^\s*[❯›>●▶→]\s`)
	trustOption = regexp.MustCompile(`^\s*([❯›>●▶→]\s+)?(\d+\.\s+)?\S`)
)

// trustKeys reads a screen and returns the keys that answer a trust prompt with yes, or nil when the screen
// isn't one (or the choice can't be told apart safely).
func trustKeys(screen string) []string {
	if !trustAsk.MatchString(screen) {
		return nil
	}
	lines := strings.Split(screen, "\n")
	for i, l := range lines { // Gemini draws its prompt in a box: "│ ● 1. Trust folder (x) │"
		lines[i] = strings.Trim(strings.TrimSpace(l), "│┃║ ")
	}
	cur, yes := -1, -1
	for i, l := range lines {
		body := trustCursor.ReplaceAllString(l, "") // an option, not the question ("Trust this folder? …")
		if yes < 0 && !strings.Contains(body, "?") && trustYes.MatchString(body) && !(trustNo.MatchString(body) && !strings.Contains(strings.ToLower(body), "yes")) {
			yes = i
		}
		if trustCursor.MatchString(l) {
			cur = i
		}
	}
	if yes < 0 {
		return nil
	}
	if cur < 0 { // no visible cursor: the first option is selected
		for i := yes; i >= 0 && trustOption.MatchString(lines[i]); i-- {
			cur = i
		}
		if cur < 0 {
			return nil
		}
	}
	if d := yes - cur; d < -5 || d > 5 { // the cursor isn't in the same option list
		return nil
	}
	var keys []string
	for i := cur; i < yes; i++ {
		keys = append(keys, "Down")
	}
	for i := cur; i > yes; i-- {
		keys = append(keys, "Up")
	}
	return append(keys, "Enter")
}

// acceptTrust watches a new terminal's screen for its first minute and answers any trust prompt with yes.
func (e *Executor) acceptTrust(ctx context.Context, t tmux, id int64) {
	name := "=" + TmuxName(id) + ":"
	deadline := time.Now().Add(time.Minute)
	answered := 0
	for time.Now().Before(deadline) && answered < 3 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(700 * time.Millisecond):
		}
		if e.wasKilled(id) {
			return
		}
		screen, err := t.run(ctx, "capture-pane", "-p", "-J", "-t", name)
		if err != nil {
			return // the session is gone
		}
		keys := trustKeys(screen)
		if keys == nil {
			continue
		}
		for _, k := range keys {
			if _, err := t.run(ctx, "send-keys", "-t", name, k); err != nil {
				return
			}
		}
		answered++
		e.logf("agent: terminal %d: trusted its folder", id)
		time.Sleep(1500 * time.Millisecond) // let the prompt go away before looking again
	}
}
