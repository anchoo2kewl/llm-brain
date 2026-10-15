package web

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
)

// TestTerminalEndToEnd opens a shell terminal through the hub with a real tmux, types into it over the
// browser's websocket, reads the output back, and ends it.
func TestTerminalEndToEnd(t *testing.T) {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("SHELL", "/bin/bash")
	rig := newSessRig(t)
	sock := fmt.Sprintf("llmb-test-%d", os.Getpid())
	t.Cleanup(func() { _ = exec.Command(tmuxBin, "-L", sock, "kill-server").Run() })
	ctx, cancel := context.WithCancel(context.Background())
	ex := &agent.Executor{Host: "studio", Version: "test", Hub: rig.srv.Agents.Hub, StateDir: t.TempDir(), HubURL: rig.ts.URL,
		TmuxSocket: sock, Poll: 100 * time.Millisecond, Providers: []string{"claude"}, Log: log.New(io.Discard, "", 0)}
	done := make(chan struct{})
	go func() { defer close(done); ex.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	resp, err := rig.cl.PostForm(rig.ts.URL+"/terminals/new", url.Values{"host": {"studio"}, "cli": {"shell"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/terminals/1" {
		t.Fatalf("new terminal: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	waitStatus := func(want string) {
		t.Helper()
		for i := 0; i < 100; i++ {
			tm, err := rig.st.Terminal(ctx, 1)
			if err == nil && tm.Status == want {
				return
			}
			if err == nil && tm.Status == "failed" {
				t.Fatalf("terminal failed: %s", tm.Error)
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("terminal never became %s", want)
	}
	waitStatus("live")

	// Another site's page can't open the websocket.
	if _, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(rig.ts.URL, "http")+"/terminals/1/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"http://evil.example"}}}); err == nil {
		t.Fatal("cross-site websocket was accepted")
	}

	wctx, wcancel := context.WithTimeout(ctx, 20*time.Second)
	defer wcancel()
	ws, _, err := websocket.Dial(wctx, "ws"+strings.TrimPrefix(rig.ts.URL, "http")+"/terminals/1/ws?cols=100&rows=30", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Write(wctx, websocket.MessageBinary, []byte("echo hub-$((40+2))\r")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for !strings.Contains(out.String(), "hub-42") {
		_, b, err := ws.Read(wctx)
		if err != nil {
			t.Fatalf("read: %v; got %q", err, out.String())
		}
		out.Write(b)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")

	// The tmux session outlived the browser; the hub lists it after the next inventory.
	if err := exec.Command(tmuxBin, "-L", sock, "has-session", "-t", "=llmb-1").Run(); err != nil {
		t.Fatal("the tmux session ended with the browser")
	}
	resp, err = rig.cl.PostForm(rig.ts.URL+"/terminals/1/kill", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitStatus("ended")
	if err := exec.Command(tmuxBin, "-L", sock, "has-session", "-t", "=llmb-1").Run(); err == nil {
		t.Fatal("the tmux session survived End")
	}
}

func TestTermScript(t *testing.T) {
	s, env, err := agent.TermScript(agent.TermOpen{Provider: "claude", Model: "opus", Prompt: "-fix it's broken"},
		agent.Gateway{URL: "http://gw", Key: "k"}, "/bin/zsh")
	if err != nil {
		t.Fatal(err)
	}
	want := `claude --dangerously-skip-permissions --model opus -- '-fix it'\''s broken'; exec /bin/zsh -l`
	if s != want {
		t.Fatalf("script\n got %s\nwant %s", s, want)
	}
	if len(env) != 2 || env[0] != "ANTHROPIC_BASE_URL=http://gw" {
		t.Fatalf("env %v", env)
	}
	if _, _, err := agent.TermScript(agent.TermOpen{Provider: "claude", Model: "x; rm -rf ~"}, agent.Gateway{}, "/bin/zsh"); err == nil {
		t.Fatal("a model name with shell in it was accepted")
	}
	s, _, _ = agent.TermScript(agent.TermOpen{Provider: "shell"}, agent.Gateway{}, "/bin/zsh")
	if s != "exec /bin/zsh -l" {
		t.Fatalf("shell script %q", s)
	}
}
