package web

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/ingest"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func TestAllowedPeer(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:5000":        true,
		"[::1]:5000":            true,
		"100.74.108.114:1":      true,
		"100.127.255.255:1":     true,
		"100.128.0.1:1":         false,
		"192.168.1.10:1":        false,
		"[fd7a:115c:a1e0::1]:1": true,
		"8.8.8.8:53":            false,
		"garbage":               false,
	} {
		if got := AllowedPeer(addr); got != want {
			t.Errorf("%s: got %v want %v", addr, got, want)
		}
	}
}

func TestPagesEscapeTranscripts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := ingest.NewEngine(st, "elastio", "elastio/blue-stack")
	evil := `<script>alert(1)</script>`
	line := `{"type":"user","sessionId":"s","timestamp":"2026-10-14T20:00:00Z","cwd":"/w","message":{"role":"user","content":"` + strings.ReplaceAll(evil, `"`, `\"`) + `"}}` + "\n"
	if _, err := eng.Chunk(context.Background(), ingest.Chunk{Host: "h", Kind: ingest.KindClaude, Path: "/p/x/s.jsonl", Mtime: time.Now(), Data: []byte(line)}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, eng, t.TempDir(), "h", log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, p := range []string{"/", "/sessions", "/sessions/1", "/search?q=alert", "/runs", "/hosts", "/sessions/1/more?after=0"} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", p, resp.StatusCode, body)
		}
		if strings.Contains(string(body), evil) {
			t.Fatalf("%s: unescaped transcript text", p)
		}
		if p != "/" && p != "/runs" && p != "/hosts" && !strings.Contains(string(body), "&lt;script&gt;") {
			t.Fatalf("%s: transcript text missing", p)
		}
	}
}
