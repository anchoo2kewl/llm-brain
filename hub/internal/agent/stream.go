package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Capture reads a turn's stdout (claude stream-json or codex --json, one event per line): it picks up the
// session id and the final answer, and renders each event as a short readable line for the turn's log.
type Capture struct {
	Provider  string
	SessionID string
	Final     string // the final answer (claude: the result event; codex: the last agent message)
	Err       string // an error the CLI reported
}

// Line takes one line of output and returns its log rendering ("" to leave it out). Lines that aren't JSON
// (stderr) come back as they are.
func (c *Capture) Line(line string) string {
	line = strings.TrimRight(line, "\r\n")
	t := strings.TrimSpace(line)
	if t == "" {
		return ""
	}
	if !strings.HasPrefix(t, "{") {
		return line
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(t), &ev); err != nil {
		return line
	}
	switch c.Provider {
	case "codex":
		return c.codex(ev)
	case "gemini":
		return c.gemini(ev)
	}
	return c.claude(ev)
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func obj(m map[string]any, k string) map[string]any {
	o, _ := m[k].(map[string]any)
	return o
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// ---------------------------------------------------------------- claude stream-json

func (c *Capture) claude(ev map[string]any) string {
	if id := str(ev, "session_id"); id != "" && c.SessionID == "" {
		c.SessionID = id
	}
	switch str(ev, "type") {
	case "system":
		if str(ev, "subtype") == "init" {
			return fmt.Sprintf("● session %s · %s · %s", str(ev, "session_id"), str(ev, "model"), str(ev, "permissionMode"))
		}
		return ""
	case "assistant":
		var out []string
		blocks, _ := obj(ev, "message")["content"].([]any)
		for _, b := range blocks {
			bm := asMap(b)
			switch str(bm, "type") {
			case "text":
				if s := strings.TrimSpace(str(bm, "text")); s != "" {
					out = append(out, "assistant: "+s)
				}
			case "tool_use":
				in, _ := json.Marshal(bm["input"])
				out = append(out, "→ "+str(bm, "name")+" "+oneLine(toolArg(bm["input"], string(in)), 200))
			}
		}
		return strings.Join(out, "\n")
	case "user":
		blocks, _ := obj(ev, "message")["content"].([]any)
		var out []string
		for _, b := range blocks {
			bm := asMap(b)
			if str(bm, "type") != "tool_result" {
				continue
			}
			text := str(bm, "content")
			if text == "" {
				if parts, ok := bm["content"].([]any); ok {
					for _, p := range parts {
						text += str(asMap(p), "text")
					}
				}
			}
			mark := "  ←"
			if e, _ := bm["is_error"].(bool); e {
				mark = "  ✗"
			}
			out = append(out, mark+" "+oneLine(text, 200))
		}
		return strings.Join(out, "\n")
	case "result":
		c.Final = str(ev, "result")
		outcome := str(ev, "subtype")
		if str(ev, "terminal_reason") == "hook_stopped" {
			outcome = "incomplete (guard stopped)"
			c.Err = "hook_stopped: a guard halted this session; work is incomplete"
		} else if e, _ := ev["is_error"].(bool); e || (str(ev, "subtype") != "" && str(ev, "subtype") != "success") {
			c.Err = strings.TrimSpace(str(ev, "subtype") + " " + oneLine(c.Final, 300))
		}
		cost, _ := ev["total_cost_usd"].(float64)
		ms, _ := ev["duration_ms"].(float64)
		return fmt.Sprintf("● result: %s in %.0fs ($%.2f)\n%s", outcome, ms/1000, cost, c.Final)
	}
	return ""
}

// toolArg is the interesting argument of a tool call: a command, a path or a pattern.
func toolArg(in any, whole string) string {
	m, _ := in.(map[string]any)
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "description"} {
		if s := str(m, k); s != "" {
			return s
		}
	}
	return whole
}

// ---------------------------------------------------------------- codex exec --json

func (c *Capture) codex(ev map[string]any) string {
	// Older codex: {"id":"0","msg":{"type":"session_configured","session_id":...}}
	if msg := obj(ev, "msg"); msg != nil {
		switch str(msg, "type") {
		case "session_configured":
			if c.SessionID == "" {
				c.SessionID = str(msg, "session_id")
			}
			return "● session " + str(msg, "session_id") + " · " + str(msg, "model")
		case "agent_message":
			c.Final = str(msg, "message")
			return "assistant: " + c.Final
		case "task_complete":
			if s := str(msg, "last_agent_message"); s != "" {
				c.Final = s
			}
			return "● turn done"
		case "exec_command_begin":
			cmd, _ := json.Marshal(msg["command"])
			return "→ $ " + oneLine(string(cmd), 200)
		case "error", "stream_error":
			c.Err = str(msg, "message")
			return "✗ " + c.Err
		}
		return ""
	}
	switch str(ev, "type") {
	case "thread.started":
		if c.SessionID == "" {
			c.SessionID = str(ev, "thread_id")
		}
		return "● session " + str(ev, "thread_id")
	case "item.completed":
		it := obj(ev, "item")
		switch str(it, "type") {
		case "agent_message", "assistant_message":
			c.Final = str(it, "text")
			return "assistant: " + c.Final
		case "command_execution":
			code := ""
			if n, ok := it["exit_code"].(float64); ok {
				code = fmt.Sprintf(" (exit %d)", int(n))
			}
			return "→ $ " + oneLine(str(it, "command"), 200) + code
		case "file_change":
			var paths []string
			ch, _ := it["changes"].([]any)
			for _, x := range ch {
				if p := str(asMap(x), "path"); p != "" {
					paths = append(paths, p)
				}
			}
			return "→ edit " + strings.Join(paths, ", ")
		case "reasoning", "":
			return ""
		case "error":
			c.Err = str(it, "message")
			return "✗ " + c.Err
		default:
			return "→ " + str(it, "type")
		}
	case "turn.completed":
		u := obj(ev, "usage")
		in, _ := u["input_tokens"].(float64)
		out, _ := u["output_tokens"].(float64)
		return fmt.Sprintf("● turn done (%.0f in / %.0f out tokens)", in, out)
	case "turn.failed":
		c.Err = str(obj(ev, "error"), "message")
		return "✗ turn failed: " + c.Err
	case "error":
		c.Err = str(ev, "message")
		return "✗ " + c.Err
	}
	return ""
}

// ---------------------------------------------------------------- gemini -o stream-json

func (c *Capture) gemini(ev map[string]any) string {
	if id := str(ev, "session_id"); id != "" && c.SessionID == "" {
		c.SessionID = id
	}
	switch str(ev, "type") {
	case "init":
		return "● session " + str(ev, "session_id") + " · " + str(ev, "model")
	case "message":
		if str(ev, "role") != "assistant" {
			return ""
		}
		text := str(ev, "content")
		if d, _ := ev["delta"].(bool); d {
			c.Final += text
		} else {
			c.Final = text
		}
		return "assistant: " + text
	case "tool_use":
		in, _ := json.Marshal(ev["parameters"])
		return "→ " + str(ev, "tool_name") + " " + oneLine(toolArg(ev["parameters"], string(in)), 200)
	case "tool_result":
		mark := "  ←"
		if str(ev, "status") == "error" {
			mark = "  ✗"
		}
		return mark + " " + oneLine(str(ev, "output"), 200)
	case "error":
		c.Err = str(ev, "message")
		return "✗ " + c.Err
	case "result":
		if str(ev, "status") != "" && str(ev, "status") != "success" {
			if c.Err == "" {
				c.Err = str(obj(ev, "error"), "message")
			}
			if c.Err == "" {
				c.Err = str(ev, "status")
			}
		}
		return "● result: " + str(ev, "status")
	}
	return ""
}
