package parse

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// Codex rollouts (~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl) come in two shapes:
//   - current: {"timestamp","type":"session_meta|response_item|event_msg|turn_context|...","payload":{...}}
//   - 2025:    a bare meta line {"id","timestamp","git"}, then bare items {"type":"message|reasoning|function_call",...}
//     with no timestamps, and {"record_type":"state"} lines.
// Conversation lives in response_item payloads; event_msg lines repeat it and are skipped.

type codexLine struct {
	Timestamp  string          `json:"timestamp"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	ID         string          `json:"id"`
	RecordType string          `json:"record_type"`
	Git        *codexGit       `json:"git"`
}

type codexGit struct {
	Branch string `json:"branch"`
}

type codexMeta struct {
	ID         string    `json:"id"`
	Cwd        string    `json:"cwd"`
	Originator string    `json:"originator"`
	Git        *codexGit `json:"git"`
}

type codexItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Summary   []codexText     `json:"summary"`
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Arguments string          `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Action    json.RawMessage `json:"action"`
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"`
	Cwd       string          `json:"cwd"`
	Model     string          `json:"model"`
}

type codexText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var envCwd = regexp.MustCompile(`<cwd>([^<]+)</cwd>`)

var codexUUID = regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

// CodexSessionID is the uuid at the end of a rollout file name.
func CodexSessionID(path string) string {
	if m := codexUUID.FindStringSubmatch(filepath.Base(path)); m != nil {
		return m[1]
	}
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// Codex parses one rollout line.
func Codex(line []byte) (Event, error) {
	var l codexLine
	if err := json.Unmarshal(line, &l); err != nil {
		return Event{}, err
	}
	ev := Event{Timestamp: parseTS(l.Timestamp)}
	switch {
	case l.RecordType != "":
		ev.Type = "state"
		return ev, nil
	case l.Payload == nil && l.Type == "" && l.ID != "": // 2025 meta line
		ev.Type = "session_meta"
		ev.SessionID = l.ID
		if l.Git != nil {
			ev.Branch = l.Git.Branch
		}
		return ev, nil
	case l.Payload == nil: // 2025 bare item
		var it codexItem
		if json.Unmarshal(line, &it) != nil {
			return ev, nil
		}
		ev.Type = it.Type
		codexItemInto(&ev, it)
	default:
		ev.Type = l.Type
		switch l.Type {
		case "session_meta":
			var m codexMeta
			if json.Unmarshal(l.Payload, &m) == nil {
				ev.SessionID, ev.Cwd, ev.Originator = m.ID, m.Cwd, m.Originator
				if m.Git != nil {
					ev.Branch = m.Git.Branch
				}
			}
			return ev, nil
		case "turn_context":
			var it codexItem
			if json.Unmarshal(l.Payload, &it) == nil {
				ev.Cwd, ev.Model = it.Cwd, it.Model
			}
			return ev, nil
		case "response_item":
			var it codexItem
			if json.Unmarshal(l.Payload, &it) == nil {
				codexItemInto(&ev, it)
			}
		default:
			return ev, nil
		}
	}
	if len(ev.Blocks) > 0 {
		ev.Raw = rawLine(line)
	}
	return ev, nil
}

func codexItemInto(ev *Event, it codexItem) {
	switch it.Type {
	case "message":
		role := it.Role
		meta := false
		if role == "developer" || role == "system" {
			role, meta = "system", true
		}
		var parts []codexText
		if json.Unmarshal(it.Content, &parts) != nil {
			return
		}
		for _, p := range parts {
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			if m := envCwd.FindStringSubmatch(p.Text); m != nil && ev.Cwd == "" {
				ev.Cwd = strings.TrimSpace(m[1])
			}
			b := Block{Role: role, Kind: "text", Meta: meta || injected(p.Text), Text: Truncate(p.Text, MaxText)}
			if role == "user" && !b.Meta && ev.FirstPrompt == "" {
				ev.FirstPrompt = ideRequest(p.Text)
			}
			ev.Blocks = append(ev.Blocks, b)
		}
	case "reasoning":
		var t []string
		for _, s := range it.Summary {
			if strings.TrimSpace(s.Text) != "" {
				t = append(t, s.Text)
			}
		}
		if len(t) > 0 {
			ev.Blocks = append(ev.Blocks, Block{Role: "assistant", Kind: "thinking", Text: Truncate(strings.Join(t, "\n\n"), MaxText)})
		}
	case "function_call", "custom_tool_call", "local_shell_call", "web_search_call", "tool_search_call":
		name := it.Name
		if it.Namespace != "" {
			name = it.Namespace + "." + name
		}
		if name == "" {
			name = strings.TrimSuffix(it.Type, "_call")
		}
		input := it.Arguments
		if input == "" {
			input = jsonText(it.Input)
		}
		if input == "" {
			input = jsonText(it.Action)
		} else if json.Valid([]byte(input)) {
			input = jsonText(json.RawMessage(input))
		}
		ev.Blocks = append(ev.Blocks, Block{Role: "assistant", Kind: "tool_use", ToolName: name, ToolUseID: it.CallID,
			ToolInput: Truncate(input, MaxToolText)})
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output", "tool_search_output":
		ev.Blocks = append(ev.Blocks, Block{Role: "user", Kind: "tool_result", ToolUseID: it.CallID,
			Text: Truncate(codexOutput(it.Output), MaxToolText)})
	}
}

// codexOutput flattens an output: a string (often JSON with an "output" field), or a list of input_text parts.
func codexOutput(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		var o struct {
			Output string `json:"output"`
		}
		if strings.HasPrefix(s, "{") && json.Unmarshal([]byte(s), &o) == nil && o.Output != "" {
			return o.Output
		}
		return s
	}
	var parts []codexText
	if json.Unmarshal(raw, &parts) == nil {
		var out []string
		for _, p := range parts {
			if p.Text != "" {
				out = append(out, p.Text)
			} else {
				out = append(out, "["+p.Type+"]")
			}
		}
		return strings.Join(out, "\n")
	}
	return string(raw)
}
