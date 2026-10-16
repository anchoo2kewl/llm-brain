package agent

import (
	"strings"
	"testing"
)

func TestTrustKeys(t *testing.T) {
	for _, c := range []struct{ name, screen, want string }{
		{"claude, cursor on no", ` Do you trust the files in this folder?
 Claude Code'll be able to read, edit, and execute files here.
 ❯ No, exit
   Yes, I trust this folder
 Enter to confirm · Esc to cancel`, "Down Enter"},
		{"claude, numbered, cursor on yes", ` Do you trust the files in this folder?
 ❯ 1. Yes, I trust this folder
   2. No, exit`, "Enter"},
		{"codex", `> You are in /Users/me/play/learn

  Do you trust the contents of this directory? Working with untrusted contents comes with higher risk of prompt injection.

› 1. Yes, continue
  2. No, quit

  Press enter to continue`, "Enter"},
		{"gemini", `Do you trust this folder?
Trusting a folder allows Gemini to execute commands it suggests.

● 1. Trust folder (learn)
  2. Trust parent folder (play)
  3. Don't trust (esc)`, "Enter"},
		{"gemini, cursor on parent", `Do you trust this folder?
  1. Trust folder (learn)
● 2. Trust parent folder (play)
  3. Don't trust (esc)`, "Up Enter"},
		{"codex 0.159", `  Trust this folder? Codex can read, edit, and run files here, subject to your permission settings.
› 1. Trust and continue
  2. Back to Agent Command Center
  enter continue · esc back`, "Enter"},
		{"gemini in a box", ` │ Do you trust this folder?                    │
 │ ● 1. Trust folder (learn)                    │
 │   2. Trust parent folder (play)              │
 │   3. Don't trust                             │
 ╰──────────────────────────────────────────────╯`, "Enter"},
		{"no prompt", "⏺ Reading package.json\n❯ ", ""},
		{"talk about trust, no option", "Do you trust the network? It depends.\n❯ ", ""},
	} {
		if got := strings.Join(trustKeys(c.screen), " "); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
