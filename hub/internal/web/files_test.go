package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
)

// TestFilesEndToEnd: list, download (kept by the hub), keep, upload and the key-folder refusal, through a
// real executor that talks back to the hub over HTTP.
func TestFilesEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, "docs"), 0o755)
	_ = os.WriteFile(filepath.Join(home, "docs", "report.md"), []byte("# hello\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte("secret"), 0o600)

	rig := newSessRig(t)
	rig.srv.StateDir = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	state := t.TempDir()
	ex := &agent.Executor{Host: "studio", Version: "test", Hub: rig.srv.Agents.Hub, StateDir: state, HubURL: rig.ts.URL,
		TmuxSocket: "llmb-files-test", Poll: 100 * time.Millisecond, Providers: []string{"claude"}, Log: log.New(io.Discard, "", 0)}
	done := make(chan struct{})
	go func() { defer close(done); ex.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	get := func(p string) (int, []byte) {
		t.Helper()
		resp, err := rig.cl.Get(rig.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	code, b := get("/hosts/studio/ls?path=" + url.QueryEscape("~/docs"))
	var l agent.Listing
	if code != 200 || json.Unmarshal(b, &l) != nil || len(l.Entries) != 1 || l.Entries[0].Name != "report.md" {
		t.Fatalf("ls: %d %s", code, b)
	}
	if code, b := get("/hosts/studio/download?path=" + url.QueryEscape("~/docs/report.md")); code != 200 || string(b) != "# hello\n" {
		t.Fatalf("download: %d %q", code, b)
	}
	as, _ := rig.st.Assets(ctx, "", 10)
	if len(as) != 1 || as[0].Source != "download" {
		t.Fatalf("download not kept: %+v", as)
	}
	_ = os.Remove(filepath.Join(home, "docs", "report.md")) // the Mac cleans up; the hub's copy stays
	if code, b := get("/files/" + strconv.FormatInt(as[0].ID, 10)); code != 200 || string(b) != "# hello\n" {
		t.Fatalf("kept copy: %d %q", code, b)
	}
	if code, b := get("/hosts/studio/download?path=" + url.QueryEscape("~/.ssh/id_ed25519")); code == 200 || strings.Contains(string(b), "secret") {
		t.Fatalf("served a key: %d %q", code, b)
	}
	if code, _ := get("/hosts/studio/ls?path=" + url.QueryEscape("/etc")); code == 200 {
		t.Fatal("listed a folder outside home")
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "screen shot.png")
	_, _ = fw.Write([]byte("PNGDATA"))
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, rig.ts.URL+"/hosts/studio/upload?into=t7", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := rig.cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var up map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&up)
	resp.Body.Close()
	if !strings.HasPrefix(up["path"], filepath.Join(state, "uploads", "t7")) || !strings.HasSuffix(up["path"], "screen-shot.png") {
		t.Fatalf("upload: %v", up)
	}
	if got, _ := os.ReadFile(up["path"]); string(got) != "PNGDATA" {
		t.Fatalf("uploaded bytes: %q", got)
	}
	if as, _ := rig.st.Assets(ctx, "screen", 10); len(as) != 1 || as[0].Source != "upload" {
		t.Fatalf("upload not kept: %+v", as)
	}
	// Another site can't push a file.
	req, _ = http.NewRequest(http.MethodPost, rig.ts.URL+"/hosts/studio/upload?into=t7", strings.NewReader("x"))
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := rig.cl.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site upload: %v %v", err, resp.StatusCode)
	}
}
