package agent

import (
	"bytes"
	"context"
	"github.com/creack/pty"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Exercise the actual outer-terminal -> tmux -> application byte path on a separate socket.
func TestControlEnterBridge(t *testing.T) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "tmux.conf")
	out := filepath.Join(dir, "key")
	if err := os.WriteFile(conf, []byte(tmuxConf), 0600); err != nil {
		t.Fatal(err)
	}
	socket := "brain-key-test-" + strconv.Itoa(os.Getpid())
	run := func(args ...string) *exec.Cmd {
		return exec.Command(bin, append([]string{"-L", socket, "-f", conf}, args...)...)
	}
	command := "stty raw -echo; printf '\\033[>4;2mREADY'; dd bs=1 count=7 of=" + shellQuote(out) + " 2>/dev/null; sleep 1"
	if b, err := run("new-session", "-d", "-s", "keytest", command).CombinedOutput(); err != nil {
		t.Fatalf("tmux: %v %s", err, b)
	}
	defer run("kill-server").Run()
	cmd := run("attach-session", "-t", "keytest")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	master, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run("kill-server").Run(); _ = master.Close(); _ = cmd.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan error, 1)
	go func() {
		var data []byte
		buf := make([]byte, 4096)
		for len(data) < 65536 {
			n, err := master.Read(buf)
			data = append(data, buf[:n]...)
			if bytes.Contains(data, []byte("READY")) {
				ready <- nil
				return
			}
			if err != nil {
				ready <- err
				return
			}
		}
		ready <- io.ErrNoProgress
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("terminal did not become ready")
	}
	want := []byte("\x1b[13;5u")
	if _, err := master.Write(want); err != nil {
		t.Fatal(err)
	}
	for {
		got, _ := os.ReadFile(out)
		if len(got) >= len(want) {
			if !bytes.Equal(got, want) {
				t.Fatalf("Control+Enter became %q", got)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Control+Enter collapsed or disappeared: %q", got)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
