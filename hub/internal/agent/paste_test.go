package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A long, multi-line text reaches the program in one bracketed paste, whole, before Enter.
func TestTypeTextPastesLongTextWhole(t *testing.T) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	conf, out, prog := filepath.Join(dir, "tmux.conf"), filepath.Join(dir, "got"), filepath.Join(dir, "reader.py")
	if err := os.WriteFile(conf, []byte(tmuxConf), 0o600); err != nil {
		t.Fatal(err)
	}
	// Like a CLI: asks for bracketed paste, shows what it reads, and stops at the Enter after the paste.
	reader := `import os, sys, tty
tty.setraw(0)
os.write(1, b"\x1b[?2004hREADY\r\n")
got = b""
while not (b"\x1b[201~" in got and not got.endswith(b"\x1b[201~")):
    b = os.read(0, 65536)
    got += b
    os.write(1, b.replace(b"\x1b[200~", b"").replace(b"\x1b[201~", b"").replace(b"\r", b"\r\n"))
open(sys.argv[1], "wb").write(got)
`
	if err := os.WriteFile(prog, []byte(reader), 0o600); err != nil {
		t.Fatal(err)
	}
	tm := tmux{bin: bin, conf: conf, sock: "brain-paste-test-" + strconv.Itoa(os.Getpid())}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const id = 9
	if _, err := tm.run(ctx, "new-session", "-d", "-x", "100", "-y", "40", "-s", TmuxName(id), shellQuote(py)+" "+shellQuote(prog)+" "+shellQuote(out)+"; sleep 5"); err != nil {
		t.Fatal(err)
	}
	defer tm.run(context.Background(), "kill-server")
	for s, _ := tm.capture(ctx, id, 0); !strings.Contains(s, "READY"); s, _ = tm.capture(ctx, id, 0) {
		select {
		case <-ctx.Done():
			t.Fatal("reader did not start")
		case <-time.After(50 * time.Millisecond):
		}
	}
	var b strings.Builder
	for i := 0; b.Len() < 5000; i++ {
		b.WriteString("-line " + strconv.Itoa(i) + " épsilon ζeta naïve\n")
	}
	text := "START " + b.String() + "END"
	if err := tm.typeText(ctx, id, "hub-send-test", text, true); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.run(ctx, "send-keys", "-t", "="+TmuxName(id)+":", "Enter"); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for got, _ = os.ReadFile(out); len(got) == 0; got, _ = os.ReadFile(out) {
		select {
		case <-ctx.Done():
			t.Fatal("reader got no Enter")
		case <-time.After(50 * time.Millisecond):
		}
	}
	want := "\x1b[200~" + strings.ReplaceAll(text, "\n", "\r") + "\x1b[201~\r"
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("got %d bytes, want %d; start %q", len(got), len(want), got[:min(len(got), 80)])
	}
}

func TestPasteShown(t *testing.T) {
	text := strings.Repeat("alpha beta ", 40) + "the end of it"
	cases := []struct {
		name, before, after string
		want                bool
	}{
		{"nothing yet", "❯ \n", "❯ \n", false},
		{"Claude placeholder", "❯ \n", "❯ [Pasted text #2]\n", true},
		{"Codex placeholder", "› [Pasted Content 10 chars]\n", "› [Pasted Content 10 chars][Pasted Content 451 chars]\n", true},
		{"wrapped and indented", "❯ \n", "❯ alpha beta alpha beta the e\n  nd of it\n", true},
		{"inside a bordered box", "│ │\n", "│ alpha beta alpha beta alpha beta the end o │\n│ f it                                       │\n", true},
		{"older placeholder scrolled off", "[Pasted text #1]\n❯ \n", "❯ [Pasted text #2]\n", true},
		{"shell continuation prompt", "$ \n", "$ alpha beta\n> alpha beta alpha beta the end of it\n", true},
		{"older copy of the end scrolled away", "said: the end of it\n❯ \n", "❯ alpha beta alpha beta the end of it\n", true},
		{"end already on screen", "said: the end of it\n", "said: the end of it\n❯ \n", false},
	}
	for _, c := range cases {
		if got := pasteShown(c.before, c.after, text); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
	if pasteShown("❯ \n", "❯ \n", "a long prompt\n|") {
		t.Error("a text ending in a border-only line was taken as shown before anything changed")
	}
	if !pasteShown("}\n❯ \n", "}\n❯ fix it:\n  func g() {\n  }\n", "fix it:\nfunc g() {\n}") {
		t.Error("a closing line already on screen hid the input's own")
	}
	if pasteShown("elapsed: 10s\n❯ \n", "elapsed: 11s\n❯ \n", "a long text\n1") {
		t.Error("a ticking clock was taken for the text's short last line")
	}
	if !pasteShown("$ \n", "$ echo abcd\n> echo efgh\n", "echo abcd\necho efgh") {
		t.Error("the last line behind a continuation prompt was missed")
	}
}
