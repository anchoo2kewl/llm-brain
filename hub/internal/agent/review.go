package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// reviewPrompt is the cross-review instruction, in the plain, short style of prompts/review.md.
const reviewPrompt = `You are reviewing work another LLM did in this folder. Your job is to catch what it missed.
You are read-only: do not edit files, commit, push, or comment anywhere.

Review the change: ` + "`git diff {{BASE}}...HEAD`" + ` (and ` + "`git log {{BASE}}..HEAD`" + `), plus the uncommitted work:
` + "`git status`" + ` and ` + "`git diff HEAD`" + `. Read surrounding code wherever needed to judge correctness.

Report only real problems, most severe first:
- correctness bugs, broken edge cases, data loss, races, migrations unsafe at production size;
- security issues (authz gaps, injection, path traversal, secrets);
- missing tests for the risky paths.
Skip style nits and anything a linter catches. Keep it short and plain.
{{RULES}}
Output format (markdown), nothing else:
## Verdict: CLEAN | ISSUES
## Findings
1. **<severity: blocker|major|minor>** ` + "`path:line`" + ` — what is wrong, the concrete failing scenario, and the fix.
`

// SendBackPrompt is the turn that hands a review's findings to the session that wrote the code.
func SendBackPrompt(findings string) string {
	return "Address these review findings: fix what is right, say briefly why if you disagree with one, and run the tests.\n\n" +
		strings.TrimSpace(findings) + "\n"
}

// BuildReviewPrompt fills the review prompt for dir against base, with the house review rules when
// bin/review-rules is there.
func BuildReviewPrompt(ctx context.Context, brainDir, dir, base string) string {
	rules := ""
	if brainDir != "" {
		rules = reviewRules(ctx, filepath.Join(brainDir, "bin", "review-rules"), dir, base)
	}
	if rules != "" {
		rules = "\nApply these review rules where they fit:\n\n" + rules + "\n"
	}
	return strings.NewReplacer("{{BASE}}", base, "{{RULES}}", rules).Replace(reviewPrompt)
}

func reviewRules(ctx context.Context, exe, dir, base string) string {
	if st, err := os.Stat(exe); err != nil || st.IsDir() {
		return ""
	}
	files := ChangedFiles(ctx, dir, base)
	if len(files) == 0 {
		return ""
	}
	if len(files) > 300 {
		files = files[:300]
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, append([]string{"--base", base, "--"}, files...)...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	if len(s) > 64<<10 {
		s = s[:64<<10] + "\n…(rules truncated)"
	}
	return s
}
