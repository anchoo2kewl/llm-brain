package parse

import (
	"encoding/json"
	"strings"
)

type claudeLine struct {
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	Timestamp   string          `json:"timestamp"`
	SessionID   string          `json:"sessionId"`
	Cwd         string          `json:"cwd"`
	GitBranch   string          `json:"gitBranch"`
	IsMeta      bool            `json:"isMeta"`
	Message     json.RawMessage `json:"message"`
	Content     json.RawMessage `json:"content"` // system events
	Summary     string          `json:"summary"`
	AITitle     string          `json:"aiTitle"`
	CustomTitle string          `json:"customTitle"`
	AgentName   string          `json:"agentName"`
	Entrypoint  string          `json:"entrypoint"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *struct {
		Input      int64 `json:"input_tokens"`
		Output     int64 `json:"output_tokens"`
		CacheRead  int64 `json:"cache_read_input_tokens"`
		CacheWrite int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// Claude parses one line of a Claude Code transcript (~/.claude/projects/<dir>/<uuid>.jsonl). Lines that are
// not conversation (file-history snapshots, mode switches, attachments) come back with no blocks but may still
// carry session metadata or a title.
func Claude(line []byte) (Event, error) {
	var l claudeLine
	if err := json.Unmarshal(line, &l); err != nil {
		return Event{}, err
	}
	ev := Event{Type: l.Type, Timestamp: parseTS(l.Timestamp), SessionID: l.SessionID, Cwd: l.Cwd, Branch: l.GitBranch,
		Originator: l.Entrypoint}
	switch l.Type {
	case "summary":
		ev.AutoTitle = l.Summary
	case "ai-title":
		ev.AutoTitle = l.AITitle
	case "custom-title":
		ev.AutoTitle = l.CustomTitle
	case "agent-name":
		ev.AgentName = l.AgentName
	case "system":
		var s string
		if json.Unmarshal(l.Content, &s) == nil && strings.TrimSpace(s) != "" {
			ev.Blocks = []Block{{Role: "system", Kind: "text", Meta: true, Text: Truncate("["+l.Subtype+"] "+s, MaxText)}}
		}
	case "user", "assistant":
		var m claudeMessage
		if len(l.Message) == 0 || json.Unmarshal(l.Message, &m) != nil {
			break
		}
		ev.Model = m.Model
		if u := m.Usage; u != nil && m.ID != "" && !strings.HasPrefix(m.Model, "<") {
			ev.Usage = &Usage{ID: m.ID, Model: m.Model, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
		}
		role := m.Role
		if role == "" {
			role = l.Type
		}
		ev.Blocks = claudeBlocks(role, m.Content, l.IsMeta)
		if role == "user" && !l.IsMeta {
			for _, b := range ev.Blocks {
				if b.Kind == "text" && !b.Meta {
					ev.FirstPrompt = b.Text
					break
				}
			}
		}
	}
	if len(ev.Blocks) > 0 {
		ev.Raw = rawLine(line)
	}
	return ev, nil
}

func claudeBlocks(role string, content json.RawMessage, meta bool) []Block {
	var s string
	if json.Unmarshal(content, &s) == nil {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return []Block{{Role: role, Kind: "text", Meta: meta || injected(s), Text: Truncate(s, MaxText)}}
	}
	var raw []claudeBlock
	if json.Unmarshal(content, &raw) != nil {
		return nil
	}
	var out []Block
	for _, b := range raw {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			out = append(out, Block{Role: role, Kind: "text", Meta: meta || injected(b.Text), Text: Truncate(b.Text, MaxText)})
		case "thinking":
			if strings.TrimSpace(b.Thinking) == "" {
				continue // redacted: only a signature is stored
			}
			out = append(out, Block{Role: role, Kind: "thinking", Text: Truncate(b.Thinking, MaxText)})
		case "redacted_thinking":
		case "tool_use", "server_tool_use":
			out = append(out, Block{Role: role, Kind: "tool_use", ToolName: b.Name, ToolUseID: b.ID,
				ToolInput: Truncate(jsonText(b.Input), MaxToolText)})
		case "tool_result":
			out = append(out, Block{Role: role, Kind: "tool_result", ToolUseID: b.ToolUseID, IsError: b.IsError,
				Text: Truncate(toolResultText(b.Content), MaxToolText)})
		case "image":
			out = append(out, Block{Role: role, Kind: "image", Meta: meta, Text: "[image]"})
		}
	}
	return out
}

// toolResultText flattens a tool_result content (a string, or a list of text/image blocks).
func toolResultText(c json.RawMessage) string {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var parts []claudeBlock
	if json.Unmarshal(c, &parts) != nil {
		return string(c)
	}
	var out []string
	for _, p := range parts {
		switch p.Type {
		case "text":
			out = append(out, p.Text)
		case "image":
			out = append(out, "[image]")
		default:
			out = append(out, "["+p.Type+"]")
		}
	}
	return strings.Join(out, "\n")
}
