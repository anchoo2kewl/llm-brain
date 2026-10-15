package web

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchoo2kewl/llm-brain/hub/internal/ingest"
	"github.com/anchoo2kewl/llm-brain/hub/internal/memory"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func TestMemoryAPI(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := New(st, ingest.NewEngine(st, "", ""), t.TempDir(), "h", log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	srv.Memory = &memory.Service{S: st}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	post := func(body string, hdr map[string]string) (int, string) {
		req, _ := http.NewRequest("POST", ts.URL+"/api/memory", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	mem := `{"kind":"gotcha","title":"Turso has no FTS5","body":"Search is LIKE over text columns; see internal/store/store.go.","scope":{"repo":"acme/hub","issue":4},"author":{"agent":"claude-code","host":"studio"}}`
	if code, body := post(mem, nil); code != http.StatusCreated || !strings.Contains(body, `"outcome":"created"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := post(mem, nil); code != http.StatusOK || !strings.Contains(body, `"outcome":"duplicate"`) {
		t.Fatalf("%d %s", code, body)
	}
	secret := `{"kind":"fact","body":"bot token xoxb-1234567890-abcdefghijkl","scope":{"repo":"acme/hub"},"author":{"host":"studio"}}`
	if code, body := post(secret, nil); code != http.StatusUnprocessableEntity || strings.Contains(body, "xoxb-") {
		t.Fatalf("secret: %d %s", code, body)
	}
	if code, _ := post(mem, map[string]string{"Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Fatalf("cross-site write: %d", code)
	}
	get := func(p string) (int, string) {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	code, body := get("/api/memory/context?repo=acme/hub&issue=%234&files=internal/store/store.go")
	var pk memory.Pack
	if code != 200 || json.Unmarshal([]byte(body), &pk) != nil || len(pk.Memories) != 1 || pk.Memories[0].Why != "this issue" {
		t.Fatalf("context: %d %s", code, body)
	}
	if code, body := get("/api/memory/context?repo=acme/hub&format=md"); code != 200 || !strings.Contains(body, "Turso has no FTS5") {
		t.Fatalf("md: %d %s", code, body)
	}
	if code, body := get("/api/presence?repo=acme/hub"); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("presence: %d %s", code, body)
	}
	if code, body := get("/memory"); code != 200 || !strings.Contains(body, "Turso has no FTS5") {
		t.Fatalf("page: %d", code)
	}
	if code, body := get("/api/memory"); code != 200 || !strings.Contains(body, "Turso has no FTS5") {
		t.Fatalf("list: %d %s", code, body)
	}
}
