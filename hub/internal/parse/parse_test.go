package parse

import (
	"strings"
	"testing"
	"time"
)

func TestClaudeUserString(t *testing.T) {
	ev, err := Claude([]byte(`{"type":"user","message":{"role":"user","content":"fix the <b>bug</b> please"},"timestamp":"2026-10-14T20:13:30.690Z","sessionId":"s1","cwd":"/Users/a/x","gitBranch":"main"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.SessionID != "s1" || ev.Cwd != "/Users/a/x" || ev.Branch != "main" || FormatTime(ev.Timestamp) != "2026-10-14T20:13:30.690Z" {
		t.Fatalf("meta: %+v", ev)
	}
	if len(ev.Blocks) != 1 || ev.Blocks[0].Kind != "text" || ev.Blocks[0].Role != "user" || ev.Blocks[0].Meta {
		t.Fatalf("blocks: %+v", ev.Blocks)
	}
	if ev.FirstPrompt != "fix the <b>bug</b> please" || ev.Raw == "" {
		t.Fatalf("prompt %q raw %q", ev.FirstPrompt, ev.Raw)
	}
}

func TestClaudeAssistantBlocks(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-10-14T20:13:34.197Z","message":{"model":"claude-opus-5-5","role":"assistant","content":[
		{"type":"thinking","thinking":"","signature":"abc"},
		{"type":"thinking","thinking":"let me look"},
		{"type":"text","text":"Reviewing now."},
		{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls -la"}}]}}`
	ev, err := Claude([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Model != "claude-opus-5-5" {
		t.Fatalf("model %q", ev.Model)
	}
	kinds := []string{}
	for _, b := range ev.Blocks {
		kinds = append(kinds, b.Kind)
	}
	if strings.Join(kinds, ",") != "thinking,text,tool_use" {
		t.Fatalf("kinds %v (redacted thinking must be dropped)", kinds)
	}
	tu := ev.Blocks[2]
	if tu.ToolName != "Bash" || tu.ToolUseID != "toolu_1" || !strings.Contains(tu.ToolInput, `"command": "ls -la"`) {
		t.Fatalf("tool_use %+v", tu)
	}
}

func TestClaudeToolResultAndImageStripping(t *testing.T) {
	big := strings.Repeat("A", 5000)
	line := `{"type":"user","timestamp":"2026-10-14T20:13:35Z","message":{"role":"user","content":[
		{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":[{"type":"text","text":"boom"},{"type":"image","source":{"type":"base64","data":"` + big + `"}}]}]}}`
	ev, err := Claude([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Blocks) != 1 {
		t.Fatalf("blocks %+v", ev.Blocks)
	}
	b := ev.Blocks[0]
	if b.Kind != "tool_result" || b.ToolUseID != "toolu_1" || !b.IsError || b.Text != "boom\n[image]" {
		t.Fatalf("result %+v", b)
	}
	if strings.Contains(ev.Raw, big) || !strings.Contains(ev.Raw, "base64 removed") {
		t.Fatalf("raw kept the image: %d bytes", len(ev.Raw))
	}
	if ev.FirstPrompt != "" {
		t.Fatalf("a tool result is not a prompt: %q", ev.FirstPrompt)
	}
}

func TestClaudeTitlesAndMeta(t *testing.T) {
	for line, want := range map[string]string{
		`{"type":"summary","summary":"Fix login bug","leafUuid":"x"}`:              "Fix login bug",
		`{"type":"ai-title","aiTitle":"Guitar solos","sessionId":"s"}`:             "Guitar solos",
		`{"type":"custom-title","customTitle":"fleet-review-x-1","sessionId":"s"}`: "fleet-review-x-1",
	} {
		ev, err := Claude([]byte(line))
		if err != nil || ev.AutoTitle != want || len(ev.Blocks) != 0 {
			t.Fatalf("%s: %+v %v", line, ev, err)
		}
	}
	ev, _ := Claude([]byte(`{"type":"agent-name","agentName":"fleet-issue-7","sessionId":"s"}`))
	if ev.AgentName != "fleet-issue-7" {
		t.Fatalf("agent %+v", ev)
	}
	ev, _ = Claude([]byte(`{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /x"}]}}`))
	if len(ev.Blocks) != 1 || !ev.Blocks[0].Meta || ev.FirstPrompt != "" {
		t.Fatalf("meta %+v", ev)
	}
	ev, _ = Claude([]byte(`{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name>"}}`))
	if !ev.Blocks[0].Meta || ev.FirstPrompt != "" {
		t.Fatalf("command wrapper should be meta: %+v", ev)
	}
	ev, _ = Claude([]byte(`{"type":"system","subtype":"away_summary","content":"while you were away"}`))
	if len(ev.Blocks) != 1 || ev.Blocks[0].Role != "system" {
		t.Fatalf("system %+v", ev)
	}
	ev, _ = Claude([]byte(`{"type":"file-history-snapshot","messageId":"m","snapshot":{}}`))
	if len(ev.Blocks) != 0 {
		t.Fatalf("snapshot %+v", ev)
	}
	if _, err := Claude([]byte(`{not json`)); err == nil {
		t.Fatal("want error on bad json")
	}
}

func TestCodexCurrentFormat(t *testing.T) {
	ev, _ := Codex([]byte(`{"timestamp":"2026-10-14T18:27:43.968Z","type":"session_meta","payload":{"id":"01a1","cwd":"/w/app","originator":"codex_exec","git":{"branch":"dev"}}}`))
	if ev.Type != "session_meta" || ev.SessionID != "01a1" || ev.Cwd != "/w/app" || ev.Originator != "codex_exec" || ev.Branch != "dev" {
		t.Fatalf("meta %+v", ev)
	}
	ev, _ = Codex([]byte(`{"timestamp":"2026-10-14T18:27:44Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/w/app</cwd>\n</environment_context>"}]}}`))
	if len(ev.Blocks) != 1 || !ev.Blocks[0].Meta || ev.Cwd != "/w/app" || ev.FirstPrompt != "" {
		t.Fatalf("env %+v", ev)
	}
	ev, _ = Codex([]byte(`{"timestamp":"2026-10-14T18:27:44Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# Context from my IDE setup:\n## Open tabs: x\n## My request for Codex:\nredesign the docs"}]}}`))
	if ev.FirstPrompt != "redesign the docs" || ev.Blocks[0].Meta {
		t.Fatalf("ide %+v", ev)
	}
	ev, _ = Codex([]byte(`{"timestamp":"2026-10-14T18:27:44Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"rules"}]}}`))
	if ev.Blocks[0].Role != "system" || !ev.Blocks[0].Meta {
		t.Fatalf("developer %+v", ev)
	}
	ev, _ = Codex([]byte(`{"timestamp":"2026-10-14T18:28:14Z","type":"response_item","payload":{"type":"function_call","name":"js","namespace":"mcp__cua","arguments":"{\"code\":\"1+1\"}","call_id":"c1"}}`))
	if b := ev.Blocks[0]; b.Kind != "tool_use" || b.ToolName != "mcp__cua.js" || b.ToolUseID != "c1" || !strings.Contains(b.ToolInput, `"code": "1+1"`) {
		t.Fatalf("call %+v", ev.Blocks)
	}
	ev, _ = Codex([]byte(`{"timestamp":"2026-10-14T18:28:16Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"Wall time: 1s"},{"type":"input_text","text":"done"}]}}`))
	if b := ev.Blocks[0]; b.Kind != "tool_result" || b.ToolUseID != "c1" || b.Text != "Wall time: 1s\ndone" {
		t.Fatalf("output %+v", ev.Blocks)
	}
	ev, _ = Codex([]byte(`{"timestamp":"2026-10-14T18:27:57Z","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"gAAA"}}`))
	if len(ev.Blocks) != 0 {
		t.Fatalf("encrypted reasoning must be dropped: %+v", ev.Blocks)
	}
	ev, _ = Codex([]byte(`{"timestamp":"2026-10-14T18:27:44Z","type":"event_msg","payload":{"type":"user_message","message":"dup"}}`))
	if len(ev.Blocks) != 0 {
		t.Fatalf("event_msg repeats response items and must be skipped: %+v", ev)
	}
}

func TestCodex2025Format(t *testing.T) {
	ev, _ := Codex([]byte(`{"id":"0650fadf-e58d","timestamp":"2025-09-16T15:23:09.701Z","instructions":null,"git":{"branch":"main"}}`))
	if ev.Type != "session_meta" || ev.SessionID != "0650fadf-e58d" || ev.Branch != "main" {
		t.Fatalf("meta %+v", ev)
	}
	ev, _ = Codex([]byte(`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"**Debugging drag-and-drop**"}]}`))
	if len(ev.Blocks) != 1 || ev.Blocks[0].Kind != "thinking" || !ev.Timestamp.IsZero() {
		t.Fatalf("reasoning %+v", ev)
	}
	ev, _ = Codex([]byte(`{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"ls\"]}","call_id":"call_R"}`))
	if ev.Blocks[0].ToolName != "shell" {
		t.Fatalf("shell %+v", ev.Blocks)
	}
	ev, _ = Codex([]byte(`{"type":"function_call_output","call_id":"call_R","output":"{\"output\":\"total 8\\n\",\"metadata\":{}}"}`))
	if ev.Blocks[0].Text != "total 8\n" {
		t.Fatalf("output %q", ev.Blocks[0].Text)
	}
	ev, _ = Codex([]byte(`{"record_type":"state"}`))
	if len(ev.Blocks) != 0 {
		t.Fatal("state line")
	}
	if id := CodexSessionID("/x/2026/10/14/rollout-2026-10-14T14-26-48-01a13baa-bf59-7782-ab13-f095a70eb666.jsonl"); id != "01a13baa-bf59-7782-ab13-f095a70eb666" {
		t.Fatalf("id %q", id)
	}
}

func TestRunLogName(t *testing.T) {
	loc, _ := time.LoadLocation("America/Toronto")
	cases := []struct {
		name, key, kind, target string
	}{
		{"issue-17442.20261014-161328.log", "issue-17442", "issue", "https://github.com/acme/web-app/issues/17442"},
		{"pr-web-app-app2-839.20261014-160917.log", "pr-web-app-app2-839", "pr", "https://github.com/acme/web-app-app2/pull/839"},
		{"review-web-app-mock-api-server-140.20261014-161328.log", "review-web-app-mock-api-server-140", "review", "https://github.com/acme/web-app-mock-api-server/pull/140"},
		{"misc.20261014-161328.log", "misc", "other", ""},
	}
	for _, c := range cases {
		r, ok := ParseRunLogName(c.name, loc, "acme", "acme/web-app")
		if !ok || r.Key != c.key || r.Kind != c.kind || r.Target != c.target {
			t.Fatalf("%s: %+v", c.name, r)
		}
	}
	r, _ := ParseRunLogName("issue-1.20261014-161328.log", loc, "acme", "acme/web-app")
	if got := FormatTime(r.Started); got != "2026-10-14T20:13:28.000Z" { // EDT is UTC-4
		t.Fatalf("started %s", got)
	}
	for _, bad := range []string{"fleet.log", "hub.log", "dashboard.log", "issue-1.2026-10-14.log", "issue-1.20261014-161328.log.bak"} {
		if _, ok := ParseRunLogName(bad, loc, "acme", "x/y"); ok {
			t.Fatalf("%s should not parse", bad)
		}
	}
}

func TestFleetLineAndReviewed(t *testing.T) {
	loc, _ := time.LoadLocation("America/Toronto")
	fl := ParseFleetLine("2026-10-14 16:20:06 [AnshumaacStudio] started issue-7 (claude/write, pid 1)", loc)
	if fl.Host != "AnshumaacStudio" || fl.Level != "notice" || FormatTime(fl.At) != "2026-10-14T20:20:06.000Z" {
		t.Fatalf("%+v", fl)
	}
	fl = ParseFleetLine(`Invalid search query "( org:acme )".`, loc)
	if !fl.At.IsZero() || fl.Level != "error" {
		t.Fatalf("%+v", fl)
	}
	r, ok := FindReviewed("Files are in x\n\nREVIEWED https://github.com/acme/web-app-app2/pull/839 APPROVE 1 inline\n")
	if !ok || r.URL != "https://github.com/acme/web-app-app2/pull/839" || r.Verdict != "APPROVE" || r.Detail != "1 inline" {
		t.Fatalf("%+v", r)
	}
	if _, ok := FindReviewed("NOT REVIEWED x"); ok {
		t.Fatal("must anchor at line start")
	}
	if LastLine("a\nb\n\n  last one  \n\n") != "last one" {
		t.Fatal("LastLine")
	}
}

func TestTruncateRuneSafe(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	out := Truncate(s, 5)
	if !strings.HasPrefix(out, "éé\n") || !strings.Contains(out, "truncated 16 bytes") {
		t.Fatalf("%q", out)
	}
}
