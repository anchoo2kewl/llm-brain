package parse

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Antigravity CLI (agy) writes one transcript per conversation:
// ~/.gemini/antigravity-cli/brain/<conversation-id>/.system_generated/logs/transcript.jsonl, one line per step:
//
//	{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"…","content":"<USER_REQUEST>…</USER_REQUEST>…"}
//	{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","model":"…","input_tokens":…,"cache_read_tokens":…,
//	 "output_tokens":…,"thinking":"…","content":"…","tool_calls":[{"name":"read_file","args":{…}}]}
//	{"step_index":2,"source":"MODEL","type":"GENERIC","content":"<tool output>"}
//
// The lines carry no cwd or branch.

type agyLine struct {
	Step      int    `json:"step_index"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Model     string `json:"model"`
	Input     int64  `json:"input_tokens"`
	CacheRead int64  `json:"cache_read_tokens"`
	Output    int64  `json:"output_tokens"`
	Thinking  string `json:"thinking"`
	Content   string `json:"content"`
	ToolCalls []struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"tool_calls"`
}

var agyRequest = regexp.MustCompile(`(?s)<USER_REQUEST>\s*(.*?)\s*</USER_REQUEST>`)

// AntigravitySessionID is the conversation id: the directory under brain/.
func AntigravitySessionID(path string) string {
	return filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path))))
}

// Antigravity parses one transcript line.
func Antigravity(line []byte) (Event, error) {
	var l agyLine
	if err := json.Unmarshal(line, &l); err != nil {
		return Event{}, err
	}
	ev := Event{Type: l.Type, Timestamp: parseTS(l.CreatedAt), Model: l.Model}
	switch l.Type {
	case "USER_INPUT":
		text := l.Content
		if m := agyRequest.FindStringSubmatch(text); m != nil {
			text = m[1] // the rest is harness metadata (local time, settings changes)
		}
		if text = strings.TrimSpace(text); text != "" {
			ev.Blocks = []Block{{Role: "user", Kind: "text", Text: Truncate(text, MaxText)}}
			if l.Source == "USER_EXPLICIT" {
				ev.FirstPrompt = text
			}
		}
	case "PLANNER_RESPONSE":
		if l.Model != "" && (l.Input > 0 || l.Output > 0 || l.CacheRead > 0) {
			// agy reports input excluding the cached part; the step index is unique within a conversation.
			ev.Usage = &Usage{ID: "step-" + strconv.Itoa(l.Step), Model: l.Model, Input: l.Input, Output: l.Output, CacheRead: l.CacheRead}
		}
		if t := strings.TrimSpace(l.Thinking); t != "" {
			ev.Blocks = append(ev.Blocks, Block{Role: "assistant", Kind: "thinking", Text: Truncate(t, MaxText)})
		}
		if t := strings.TrimSpace(l.Content); t != "" {
			ev.Blocks = append(ev.Blocks, Block{Role: "assistant", Kind: "text", Text: Truncate(t, MaxText)})
		}
		for i, tc := range l.ToolCalls {
			ev.Blocks = append(ev.Blocks, Block{Role: "assistant", Kind: "tool_use", ToolName: tc.Name,
				ToolUseID: strconv.Itoa(l.Step) + "." + strconv.Itoa(i), ToolInput: Truncate(jsonText(tc.Args), MaxToolText)})
		}
	default: // GENERIC and the other step kinds: a tool's output or a system note
		if t := strings.TrimSpace(l.Content); t != "" {
			ev.Blocks = []Block{{Role: "user", Kind: "tool_result", Text: Truncate(t, MaxToolText), IsError: l.Status == "ERROR"}}
		}
	}
	if len(ev.Blocks) > 0 {
		ev.Raw = rawLine(line)
	}
	return ev, nil
}
