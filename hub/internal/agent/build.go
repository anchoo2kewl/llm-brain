package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// Gateway routes a turn through the account gateway the way bin/llm-run does. A zero Gateway leaves the
// CLI on its own login.
type Gateway struct {
	URL string // e.g. http://127.0.0.1:8317
	Key string // CPA_API_KEY
}

func (g Gateway) On() bool { return g.URL != "" && g.Key != "" }

// Spec is one process to start.
type Spec struct {
	Bin      string   // claude | codex, looked up on PATH
	Args     []string // never holds the prompt: it goes on stdin
	Env      []string // added to the environment, KEY=VALUE
	Dir      string
	Stdin    string
	LastFile string // codex: where --output-last-message writes the final answer
}

// reviewTools: what a read-only claude review may run.
const reviewTools = "Read,Grep,Glob,Bash(git diff:*),Bash(git log:*),Bash(git show:*),Bash(git status:*),Bash(git merge-base:*)"

var (
	modelRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,99}$`)
	sessionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	nameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)
)

// NormPerm maps form values (and codex's sandbox names) onto the three modes; ” is the default.
func NormPerm(p string) (string, error) {
	switch p {
	case "", PermEdit, "workspace-write", "edit":
		return PermEdit, nil
	case PermPlan, "read-only":
		return PermPlan, nil
	case PermBypass, "bypassPermissions", "danger-full-access":
		return PermBypass, nil
	}
	return "", fmt.Errorf("unknown permission mode %q", p)
}

// PermLabel is the mode as the provider calls it.
func PermLabel(provider, perm string) string {
	if provider == "codex" {
		switch perm {
		case PermPlan:
			return "read-only"
		case PermEdit:
			return "workspace-write"
		case PermBypass:
			return "bypass (no sandbox)"
		}
	}
	if perm == PermBypass {
		return "bypassPermissions"
	}
	return perm
}

func checkTurn(t Turn) error {
	if t.Model != "" && !modelRe.MatchString(t.Model) {
		return fmt.Errorf("bad model name %q", t.Model)
	}
	if t.Resume != "" && !sessionRe.MatchString(t.Resume) {
		return fmt.Errorf("bad session id %q", t.Resume)
	}
	if t.Name != "" && !nameRe.MatchString(t.Name) {
		return fmt.Errorf("bad session name %q", t.Name)
	}
	if t.Perm == PermBypass && !t.Bypass {
		return fmt.Errorf("bypass mode needs explicit consent for this session")
	}
	if _, err := NormPerm(t.Perm); err != nil {
		return err
	}
	return nil
}

// BuildClaude: claude -p --output-format stream-json --verbose [--resume id] ..., the prompt on stdin.
func BuildClaude(t Turn, cwd, prompt string, gw Gateway) (Spec, error) {
	if err := checkTurn(t); err != nil {
		return Spec{}, err
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if t.Resume != "" {
		args = append(args, "--resume", t.Resume)
	} else if t.Name != "" {
		args = append(args, "--name", t.Name)
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	switch perm, _ := NormPerm(t.Perm); perm {
	case PermPlan:
		args = append(args, "--permission-mode", "plan")
		if t.Review != nil {
			args = append(args, "--allowedTools", reviewTools)
		}
	case PermEdit:
		args = append(args, "--permission-mode", "acceptEdits")
	case PermBypass:
		args = append(args, "--dangerously-skip-permissions")
	}
	s := Spec{Bin: "claude", Args: args, Dir: cwd, Stdin: prompt}
	if gw.On() {
		s.Env = []string{"ANTHROPIC_BASE_URL=" + gw.URL, "ANTHROPIC_AUTH_TOKEN=" + gw.Key}
	}
	return s, nil
}

// BuildCodex: codex exec [resume <id>] --json ..., the prompt on stdin ("-").
func BuildCodex(t Turn, cwd, prompt, lastFile string, gw Gateway) (Spec, error) {
	if err := checkTurn(t); err != nil {
		return Spec{}, err
	}
	perm, _ := NormPerm(t.Perm)
	args := []string{"exec"}
	if t.Resume != "" {
		// resume takes no --sandbox / -C: the sandbox goes in as config, the folder is the process's cwd.
		args = append(args, "resume", "--json", "--skip-git-repo-check")
		switch perm {
		case PermPlan:
			args = append(args, "-c", `sandbox_mode="read-only"`)
		case PermEdit:
			args = append(args, "-c", `sandbox_mode="workspace-write"`)
		}
	} else {
		args = append(args, "--json", "--skip-git-repo-check", "-C", cwd)
		switch perm {
		case PermPlan:
			args = append(args, "--sandbox", "read-only")
		case PermEdit:
			args = append(args, "--sandbox", "workspace-write")
		}
	}
	if perm == PermBypass {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	if t.Model != "" {
		args = append(args, "-m", t.Model)
	}
	var env []string
	if gw.On() {
		args = append(args, "-c", "model_provider=cpa",
			"-c", fmt.Sprintf(`model_providers.cpa={name="CPA",base_url="%s/v1",env_key="CPA_API_KEY",wire_api="responses",requires_openai_auth=false}`,
				strings.TrimRight(gw.URL, "/")))
		env = []string{"CPA_API_KEY=" + gw.Key}
	}
	if lastFile != "" {
		args = append(args, "-o", lastFile)
	}
	if t.Resume != "" {
		args = append(args, t.Resume)
	}
	args = append(args, "-")
	return Spec{Bin: "codex", Args: args, Env: env, Dir: cwd, Stdin: prompt, LastFile: lastFile}, nil
}

// BuildGemini: gemini --prompt=<prompt> -o stream-json --approval-mode ... [--resume id]. Gemini reads the
// prompt from the flag (stdin is only appended to it); the = form keeps a prompt starting with '-' a value.
// It runs on the CLI's own login: the account gateway speaks the Anthropic and OpenAI APIs.
func BuildGemini(t Turn, cwd, prompt string) (Spec, error) {
	if err := checkTurn(t); err != nil {
		return Spec{}, err
	}
	args := []string{"--prompt=" + prompt, "--output-format", "stream-json"}
	if t.Resume != "" {
		args = append(args, "--resume", t.Resume)
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	switch perm, _ := NormPerm(t.Perm); perm {
	case PermPlan:
		args = append(args, "--approval-mode", "plan")
	case PermEdit:
		args = append(args, "--approval-mode", "auto_edit")
	case PermBypass:
		args = append(args, "--approval-mode", "yolo")
	}
	return Spec{Bin: "gemini", Args: args, Dir: cwd}, nil
}

// Providers are the agent CLIs the hub can drive.
var Providers = []string{"claude", "codex", "gemini"}

func KnownProvider(p string) bool {
	for _, x := range Providers {
		if x == p {
			return true
		}
	}
	return false
}

// ProviderName is how pages name a provider.
func ProviderName(p string) string {
	switch p {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex (OpenAI)"
	case "gemini":
		return "Gemini CLI"
	}
	return p
}

// ResumeCommand is what to type on that Mac to continue the session by hand.
func ResumeCommand(provider, cwd, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	q := shellQuote(cwd)
	switch provider {
	case "codex":
		return "cd " + q + " && codex resume " + sessionID
	case "gemini":
		return "cd " + q + " && gemini --resume " + sessionID
	}
	return "cd " + q + " && claude --resume " + sessionID
}

func shellQuote(s string) string {
	if s != "" && regexp.MustCompile(`^[A-Za-z0-9_./~@%+=:,-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
