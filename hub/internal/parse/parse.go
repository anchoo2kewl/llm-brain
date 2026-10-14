// Package parse turns transcript lines and fleet log names into plain records. It has no I/O and no state, so
// every parser is a pure function of its input and is unit tested on its own.
package parse

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Size caps. Transcripts hold whole file reads and command outputs; the hub keeps enough to read the
// conversation, not a second copy of every file the agent looked at.
const (
	MaxToolText = 20 << 10  // tool input / tool result
	MaxText     = 200 << 10 // one text block
	MaxRaw      = 256 << 10 // the raw JSON line, image data stripped
)

// Block is one renderable piece of a conversation event.
type Block struct {
	Role      string // user | assistant | system
	Kind      string // text | thinking | tool_use | tool_result | image
	Meta      bool   // injected by the harness (skill bodies, environment context), not typed by a person
	Text      string
	ToolName  string
	ToolUseID string
	ToolInput string
	IsError   bool
}

// Event is one transcript line, reduced to what the hub stores.
type Event struct {
	Type      string // the line's type (user, assistant, summary, session_meta, ...)
	Timestamp time.Time
	SessionID string
	Cwd       string
	Branch    string
	Model     string
	// Title hints. FirstPrompt is a person-typed prompt; AutoTitle is a generated one (summary, ai-title,
	// custom-title, agent name).
	FirstPrompt string
	AutoTitle   string
	AgentName   string
	Originator  string
	Blocks      []Block
	Raw         string
}

// Time is the hub's one time format: UTC, fixed width, so text comparison is time comparison.
const Time = "2006-01-02T15:04:05.000Z"

func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(Time)
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, f := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Truncate cuts s to at most n bytes on a rune boundary and says how much was dropped.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n… [truncated " + itoa(len(s)-cut) + " bytes]"
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// rawLine returns the line with base64 image payloads removed and capped at MaxRaw.
func rawLine(line []byte) string {
	if bytes.Contains(line, []byte(`"base64"`)) {
		var v any
		if json.Unmarshal(line, &v) == nil {
			stripImages(v)
			if b, err := json.Marshal(v); err == nil {
				line = b
			}
		}
	}
	return Truncate(string(line), MaxRaw)
}

func stripImages(v any) {
	switch x := v.(type) {
	case map[string]any:
		if x["type"] == "base64" {
			if d, ok := x["data"].(string); ok {
				x["data"] = "[" + itoa(len(d)) + " bytes of base64 removed]"
			}
		}
		for _, c := range x {
			stripImages(c)
		}
	case []any:
		for _, c := range x {
			stripImages(c)
		}
	}
}

// jsonText renders a tool input as compact JSON (or the string itself when it already is one).
func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") == nil {
		return buf.String()
	}
	return string(raw)
}

// injected reports whether a "user" text is harness context rather than something a person typed.
func injected(s string) bool {
	t := strings.TrimSpace(s)
	if m := wrapperTag.FindStringSubmatch(t); m != nil && m[1] != "pasted_content" && strings.HasSuffix(t, "</"+m[1]+">") {
		return true // <some_harness_tag>…</some_harness_tag>
	}
	for _, p := range []string{"<environment_context>", "<user_instructions>", "<app-context>", "<system-reminder>",
		"<command-name>", "<command-message>", "<local-command-", "<permissions instructions>", "<user_shell_command>",
		"Base directory for this skill:", "Caveat: The messages below", "[Image: source:", "<task-notification>",
		"<turn_aborted>", "# AGENTS.md instructions", "<INSTRUCTIONS>", "<collaboration_mode>"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

var pastedTag = regexp.MustCompile(`</?pasted_content[^>]*>`)

var wrapperTag = regexp.MustCompile(`^<([a-z][a-z0-9]*[_-][a-z0-9_-]*)[ >]`) // harness tags have a _ or -; <div> does not

// ideRequest pulls the person's words out of a Codex IDE prompt ("# Context from my IDE setup: … ## My request
// for Codex: …").
func ideRequest(s string) string {
	const marker = "## My request for Codex:"
	if strings.HasPrefix(strings.TrimSpace(s), "# Context from my IDE setup:") {
		if i := strings.Index(s, marker); i >= 0 {
			return strings.TrimSpace(s[i+len(marker):])
		}
	}
	return s
}

// TitleFrom shortens a prompt to a one-line title.
func TitleFrom(s string) string {
	s = pastedTag.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	return Truncate(s, 160)
}
