package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

type recHub struct{ reps []Report }

func (h *recHub) Poll(context.Context, string, string, []int64) ([]store.Command, error) { return nil, nil }
func (h *recHub) Report(_ context.Context, _ int64, r Report) error                         { h.reps = append(h.reps, r); return nil }
func (h *recHub) Inventory(context.Context, Inventory) error                                { return nil }

func TestSessionTitleAppendsCustomTitle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	uuid := "6f346204-3375-4b69-9491-fb6e4c6bdc30"
	dir := filepath.Join(home, ".claude", "projects", "-x")
	_ = os.MkdirAll(dir, 0o755)
	tr := filepath.Join(dir, uuid+".jsonl")
	_ = os.WriteFile(tr, []byte(`{"type":"user"}`), 0o644) // no trailing newline
	h := &recHub{}
	e := &Executor{Hub: h}
	run := func(p SessionTitle) Report {
		b, _ := json.Marshal(p)
		h.reps = nil
		e.sessionTitle(context.Background(), store.Command{ID: 1, Payload: b})
		return h.reps[len(h.reps)-1]
	}
	if r := run(SessionTitle{Path: tr, UUID: uuid, Title: "Meircom check"}); r.Status != "done" {
		t.Fatalf("title: %+v", r)
	}
	b, _ := os.ReadFile(tr)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || lines[1] != `{"customTitle":"Meircom check","sessionId":"`+uuid+`","type":"custom-title"}` {
		t.Fatalf("file: %q", b)
	}
	other := filepath.Join(home, "notes.jsonl")
	_ = os.WriteFile(other, nil, 0o644)
	for _, p := range []SessionTitle{{Path: other, UUID: uuid, Title: "x"}, {Path: filepath.Join(dir, "other.jsonl"), UUID: uuid, Title: "x"}, {Path: tr, UUID: "nope", Title: "x"}} {
		if r := run(p); r.Status != "failed" {
			t.Fatalf("accepted %+v", p)
		}
	}
}

func TestTermScriptSessionID(t *testing.T) {
	s, _, err := TermScript(TermOpen{Provider: "claude", SessionID: "6f346204-3375-4b69-9491-fb6e4c6bdc30"}, Gateway{}, "/bin/zsh")
	if err != nil || !strings.Contains(s, "--session-id 6f346204-3375-4b69-9491-fb6e4c6bdc30") {
		t.Fatalf("%q %v", s, err)
	}
	if _, _, err := TermScript(TermOpen{Provider: "claude", SessionID: "x; rm -rf ~"}, Gateway{}, "/bin/zsh"); err == nil {
		t.Fatal("bad session id accepted")
	}
	if u := newUUID(); !uuidRe.MatchString(u) {
		t.Fatalf("uuid %q", u)
	}
}
